package graphql_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/graphql"
	"github.com/ngaut/agent-git-service/internal/service"
)

func seedOrgForQueryTest(t *testing.T, svc *service.Service, login string) db.User {
	t.Helper()
	org := db.User{Login: login, Name: login, Type: db.TypeOrganization}
	require.NoError(t, svc.DB.Create(&org).Error)
	return org
}

func addOrgMemberForQueryTest(t *testing.T, svc *service.Service, ctx context.Context, org, member db.User) {
	t.Helper()
	require.NoError(t, svc.DB.AutoMigrate(&db.OrganizationMember{}))
	require.NoError(t, svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember))
}

func orgLoginsFromNodes(t *testing.T, nodes []any) []string {
	t.Helper()
	out := make([]string, 0, len(nodes))
	for _, raw := range nodes {
		m, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("org node is not object: %#v", raw)
		}
		login, _ := m["login"].(string)
		out = append(out, login)
	}
	return out
}

// =============================================================================
// doViewerWithOrgs Tests
// =============================================================================

// TestDoViewerWithOrgs_Success tests viewer query with organizations
func TestDoViewerWithOrgs_Success(t *testing.T) {
	svc, mux, user, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	org := seedOrgForQueryTest(t, svc, "viewer-org")
	addOrgMemberForQueryTest(t, svc, ctx, org, user)

	q := `
	query {
		viewer {
			login
			name
			organizations(first: 10) {
				nodes {
					login
					name
				}
			}
		}
	}`
	data := doGql(t, mux, q, nil)
	viewer := data["viewer"].(map[string]any)

	require.Equal(t, "tester", viewer["login"])
	require.Equal(t, "tester", viewer["name"])

	orgs := viewer["organizations"].(map[string]any)
	nodes := orgs["nodes"].([]any)
	require.Len(t, nodes, 1, "should return only the membership that was created")
	require.Equal(t, org.Login, nodes[0].(map[string]any)["login"])
}

// TestDoViewerWithOrgs_EmptyOrgs tests viewer query when user has no organizations
func TestDoViewerWithOrgs_EmptyOrgs(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	q := `
	query {
		viewer {
			login
			organizations(first: 10) {
				nodes {
					login
					name
				}
			}
		}
	}`
	data := doGql(t, mux, q, nil)
	viewer := data["viewer"].(map[string]any)

	require.Equal(t, "tester", viewer["login"])

	orgs := viewer["organizations"].(map[string]any)
	nodes := orgs["nodes"].([]any)
	require.Len(t, nodes, 0, "should have no organizations")
}

// TestDoViewerWithOrgs_MultipleOrgs tests viewer with multiple organizations
func TestDoViewerWithOrgs_MultipleOrgs(t *testing.T) {
	svc, mux, user, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	orgA := seedOrgForQueryTest(t, svc, "org-alpha")
	_ = seedOrgForQueryTest(t, svc, "org-beta")
	orgC := seedOrgForQueryTest(t, svc, "org-gamma")
	addOrgMemberForQueryTest(t, svc, ctx, orgA, user)
	addOrgMemberForQueryTest(t, svc, ctx, orgC, user)

	q := `
	query {
		viewer {
			login
			organizations(first: 10) {
				nodes {
					login
					name
				}
			}
		}
	}`
	data := doGql(t, mux, q, nil)
	viewer := data["viewer"].(map[string]any)

	orgs := viewer["organizations"].(map[string]any)
	nodes := orgs["nodes"].([]any)
	require.Len(t, nodes, 2, "should only return the memberships that were created")
	require.ElementsMatch(t, []string{orgA.Login, orgC.Login}, orgLoginsFromNodes(t, nodes))
}

// =============================================================================
// doUserWithOrgs Tests
// =============================================================================

// TestDoUserWithOrgs_Success tests user query with organizations
func TestDoUserWithOrgs_Success(t *testing.T) {
	svc, mux, viewer, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	viewerOrg := seedOrgForQueryTest(t, svc, "viewer-only-org")
	addOrgMemberForQueryTest(t, svc, ctx, viewerOrg, viewer)

	target := db.User{Login: "other-user", Name: "Other User", Type: db.TypeUser}
	require.NoError(t, svc.DB.Create(&target).Error)
	targetOrg := seedOrgForQueryTest(t, svc, "target-org")
	addOrgMemberForQueryTest(t, svc, ctx, targetOrg, target)

	q := `
	query($user: String!) {
		user(login: $user) {
			login
			organizations(first: 10) {
				nodes {
					login
					name
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{"user": target.Login})
	user := data["user"].(map[string]any)

	require.Equal(t, target.Login, user["login"])

	orgs := user["organizations"].(map[string]any)
	nodes := orgs["nodes"].([]any)
	require.Len(t, nodes, 1, "should only return the target user's organizations")
	require.Equal(t, targetOrg.Login, nodes[0].(map[string]any)["login"])
}

// TestDoUserWithOrgs_NotFound tests user query for non-existent user
func TestDoUserWithOrgs_NotFound(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	q := `
	query($user: String!) {
		user(login: $user) {
			login
		}
	}`
	data := doGql(t, mux, q, map[string]any{"user": "nonexistent"})
	user := data["user"]

	require.Nil(t, user, "should return nil for non-existent user")
}

// TestDoUserWithOrgs_AliasLogin tests user query with login variable alias
func TestDoUserWithOrgs_AliasLogin(t *testing.T) {
	svc, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	target := db.User{Login: "alias-user", Name: "Alias User", Type: db.TypeUser}
	require.NoError(t, svc.DB.Create(&target).Error)
	org := seedOrgForQueryTest(t, svc, "alias-org")
	addOrgMemberForQueryTest(t, svc, ctx, org, target)

	// Test with "login" variable instead of "user"
	q := `
	query($login: String!) {
		user(login: $login) {
			login
			organizations(first: 10) {
				nodes {
					login
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{"login": target.Login})
	user := data["user"].(map[string]any)

	require.Equal(t, target.Login, user["login"])

	orgs := user["organizations"].(map[string]any)
	nodes := orgs["nodes"].([]any)
	require.Len(t, nodes, 1, "should return the alias user's organizations")
	require.Equal(t, org.Login, nodes[0].(map[string]any)["login"])
}

// =============================================================================
// doRepositoryOwner Tests
// =============================================================================

// TestDoRepositoryOwner_Success tests repository owner query
func TestDoRepositoryOwner_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create a repository
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "owner-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	q := `
	query($owner: String!) {
		repositoryOwner(login: $owner) {
			login
			repositories(first: 10) {
				nodes {
					name
					nameWithOwner
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{"owner": "tester"})
	owner := data["repositoryOwner"].(map[string]any)

	require.Equal(t, "tester", owner["login"])

	repos := owner["repositories"].(map[string]any)
	nodes := repos["nodes"].([]any)
	require.GreaterOrEqual(t, len(nodes), 1, "should have at least one repository")
	node := nodes[0].(map[string]any)
	require.Equal(t, "owner-repo", node["name"])
}

// TestDoRepositoryOwner_ViewerCase tests repository owner query without owner (viewer)
func TestDoRepositoryOwner_ViewerCase(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create a repository for viewer
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "viewer-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	q := `
	query {
		repositoryOwner {
			login
			repositories(first: 10) {
				nodes {
					name
				}
			}
		}
	}`
	data := doGql(t, mux, q, nil)
	owner := data["repositoryOwner"].(map[string]any)

	require.Equal(t, "tester", owner["login"])

	repos := owner["repositories"].(map[string]any)
	nodes := repos["nodes"].([]any)
	require.GreaterOrEqual(t, len(nodes), 1, "should have at least one repository")
}

// TestDoRepositoryOwner_NotFound tests repository owner query for non-existent owner
func TestDoRepositoryOwner_NotFound(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	q := `
	query($owner: String!) {
		repositoryOwner(login: $owner) {
			login
		}
	}`
	data := doGql(t, mux, q, map[string]any{"owner": "nonexistent"})
	owner := data["repositoryOwner"]

	require.Nil(t, owner, "should return nil for non-existent owner")
}

// TestDoRepositoryOwner_EmptyRepos tests repository owner with no repositories
func TestDoRepositoryOwner_EmptyRepos(t *testing.T) {
	svc, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Create a user but no repositories
	otherUser := db.User{Login: "empty-user", Name: "Empty User", Type: db.TypeUser}
	svc.DB.Create(&otherUser)

	q := `
	query($owner: String!) {
		repositoryOwner(login: $owner) {
			login
			repositories(first: 10) {
				nodes {
					name
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{"owner": "empty-user"})
	owner := data["repositoryOwner"].(map[string]any)

	require.Equal(t, "empty-user", owner["login"])

	repos := owner["repositories"].(map[string]any)
	nodes := repos["nodes"].([]any)
	require.Len(t, nodes, 0, "should have no repositories")
}

// =============================================================================
// DoTypeIntrospection Tests
// =============================================================================

// TestDoTypeIntrospection_Issue tests introspection for Issue type
func TestDoTypeIntrospection_Issue(t *testing.T) {
	types := map[string]string{"issueType": "Issue"}
	result := graphql.DoTypeIntrospection(types)

	require.NotNil(t, result["data"])
	data := result["data"].(map[string]any)
	require.NotNil(t, data["issueType"])

	issueType := data["issueType"].(map[string]any)
	fields := issueType["fields"].([]any)
	require.Greater(t, len(fields), 0, "Issue should have fields")

	// Check for key fields
	fieldNames := make(map[string]bool)
	for _, f := range fields {
		fieldMap := f.(map[string]any)
		fieldNames[fieldMap["name"].(string)] = true
	}

	requiredFields := []string{"id", "title", "body", "state", "number", "author"}
	for _, rf := range requiredFields {
		require.True(t, fieldNames[rf], "Issue should have field: %s", rf)
	}
}

// TestDoTypeIntrospection_PullRequest tests introspection for PullRequest type
func TestDoTypeIntrospection_PullRequest(t *testing.T) {
	types := map[string]string{"prType": "PullRequest"}
	result := graphql.DoTypeIntrospection(types)

	data := result["data"].(map[string]any)
	prType := data["prType"].(map[string]any)
	fields := prType["fields"].([]any)

	fieldNames := make(map[string]bool)
	for _, f := range fields {
		fieldMap := f.(map[string]any)
		fieldNames[fieldMap["name"].(string)] = true
	}

	requiredFields := []string{"id", "title", "state", "number", "merged", "mergeable", "headRefName", "baseRefName"}
	for _, rf := range requiredFields {
		require.True(t, fieldNames[rf], "PullRequest should have field: %s", rf)
	}
}

// TestDoTypeIntrospection_Repository tests introspection for Repository type
func TestDoTypeIntrospection_Repository(t *testing.T) {
	types := map[string]string{"repoType": "Repository"}
	result := graphql.DoTypeIntrospection(types)

	data := result["data"].(map[string]any)
	repoType := data["repoType"].(map[string]any)
	fields := repoType["fields"].([]any)

	fieldNames := make(map[string]bool)
	for _, f := range fields {
		fieldMap := f.(map[string]any)
		fieldNames[fieldMap["name"].(string)] = true
	}

	requiredFields := []string{"id", "name", "nameWithOwner", "description", "isPrivate", "owner"}
	for _, rf := range requiredFields {
		require.True(t, fieldNames[rf], "Repository should have field: %s", rf)
	}
}

// TestDoTypeIntrospection_MultipleTypes tests introspection for multiple types at once
func TestDoTypeIntrospection_MultipleTypes(t *testing.T) {
	types := map[string]string{
		"issue": "Issue",
		"pr":    "PullRequest",
		"repo":  "Repository",
	}
	result := graphql.DoTypeIntrospection(types)

	data := result["data"].(map[string]any)
	require.Len(t, data, 3, "should have introspection for all 3 types")

	require.NotNil(t, data["issue"])
	require.NotNil(t, data["pr"])
	require.NotNil(t, data["repo"])
}

// TestDoTypeIntrospection_UnknownType tests introspection for unknown type (defaults to id)
func TestDoTypeIntrospection_UnknownType(t *testing.T) {
	types := map[string]string{"unknown": "SomeUnknownType"}
	result := graphql.DoTypeIntrospection(types)

	data := result["data"].(map[string]any)
	unknownType := data["unknown"].(map[string]any)
	fields := unknownType["fields"].([]any)

	require.Len(t, fields, 1, "unknown type should default to just id field")
	fieldMap := fields[0].(map[string]any)
	require.Equal(t, "id", fieldMap["name"])
}

// TestDoTypeIntrospection_ProjectV2 tests introspection for ProjectV2 type with args
func TestDoTypeIntrospection_ProjectV2(t *testing.T) {
	types := map[string]string{"project": "ProjectV2"}
	result := graphql.DoTypeIntrospection(types)

	data := result["data"].(map[string]any)
	projectType := data["project"].(map[string]any)
	fields := projectType["fields"].([]any)

	fieldNames := make(map[string]any)
	for _, f := range fields {
		fieldMap := f.(map[string]any)
		fieldNames[fieldMap["name"].(string)] = fieldMap
	}

	// Check items field has args
	itemsField, ok := fieldNames["items"]
	require.True(t, ok, "ProjectV2 should have items field")
	itemsMap := itemsField.(map[string]any)
	args, hasArgs := itemsMap["args"]
	require.True(t, hasArgs, "items field should have args")
	argsList := args.([]any)
	require.Greater(t, len(argsList), 0, "items should have at least one arg")
}

// TestDoTypeIntrospection_SearchType tests introspection for SearchType enum
func TestDoTypeIntrospection_SearchType(t *testing.T) {
	types := map[string]string{"searchType": "SearchType"}
	result := graphql.DoTypeIntrospection(types)

	data := result["data"].(map[string]any)
	searchType := data["searchType"].(map[string]any)
	enumValues := searchType["enumValues"].([]any)

	require.Greater(t, len(enumValues), 0, "SearchType should have enum values")

	valueNames := make(map[string]bool)
	for _, v := range enumValues {
		valueMap := v.(map[string]any)
		valueNames[valueMap["name"].(string)] = true
	}

	// Check for expected enum values
	require.True(t, valueNames["ISSUE"], "SearchType should have ISSUE enum value")
	require.True(t, valueNames["REPOSITORY"], "SearchType should have REPOSITORY enum value")
}

// TestDoTypeIntrospection_StatusCheckRollup tests introspection for StatusCheckRollup
func TestDoTypeIntrospection_StatusCheckRollup(t *testing.T) {
	types := map[string]string{"statusCheck": "StatusCheckRollupContextConnection"}
	result := graphql.DoTypeIntrospection(types)

	data := result["data"].(map[string]any)
	statusType := data["statusCheck"].(map[string]any)
	fields := statusType["fields"].([]any)

	fieldNames := make(map[string]bool)
	for _, f := range fields {
		fieldMap := f.(map[string]any)
		fieldNames[fieldMap["name"].(string)] = true
	}

	require.True(t, fieldNames["checkRunCount"], "StatusCheckRollup should have checkRunCount")
	require.True(t, fieldNames["statusContextCount"], "StatusCheckRollup should have statusContextCount")
}

// =============================================================================
// TypeFields Tests
// =============================================================================

// TestTypeFields_Issue tests TypeFields for Issue
func TestTypeFields_Issue(t *testing.T) {
	result := graphql.TypeFields("Issue")
	require.NotNil(t, result["fields"])
	fields := result["fields"].([]any)
	require.Greater(t, len(fields), 10, "Issue should have many fields")
}

// TestTypeFields_PullRequest tests TypeFields for PullRequest
func TestTypeFields_PullRequest(t *testing.T) {
	result := graphql.TypeFields("PullRequest")
	require.NotNil(t, result["fields"])
	fields := result["fields"].([]any)
	require.Greater(t, len(fields), 15, "PullRequest should have many fields")
}

// TestTypeFields_Repository tests TypeFields for Repository
func TestTypeFields_Repository(t *testing.T) {
	result := graphql.TypeFields("Repository")
	require.NotNil(t, result["fields"])
	fields := result["fields"].([]any)
	require.Greater(t, len(fields), 10, "Repository should have many fields")
}

// TestTypeFields_Release tests TypeFields for Release
func TestTypeFields_Release(t *testing.T) {
	result := graphql.TypeFields("Release")
	require.NotNil(t, result["fields"])
	fields := result["fields"].([]any)

	fieldNames := make(map[string]bool)
	for _, f := range fields {
		fieldMap := f.(map[string]any)
		fieldNames[fieldMap["name"].(string)] = true
	}

	require.True(t, fieldNames["tagName"], "Release should have tagName")
	require.True(t, fieldNames["isDraft"], "Release should have isDraft")
	require.True(t, fieldNames["isPrerelease"], "Release should have isPrerelease")
}

// TestTypeFields_LinkedBranch tests TypeFields for LinkedBranch
func TestTypeFields_LinkedBranch(t *testing.T) {
	result := graphql.TypeFields("LinkedBranch")
	require.NotNil(t, result["fields"])
	fields := result["fields"].([]any)

	fieldNames := make(map[string]bool)
	for _, f := range fields {
		fieldMap := f.(map[string]any)
		fieldNames[fieldMap["name"].(string)] = true
	}

	require.True(t, fieldNames["ref"], "LinkedBranch should have ref")
}

// TestTypeFields_Unknown tests TypeFields for unknown type (defaults to id)
func TestTypeFields_Unknown(t *testing.T) {
	result := graphql.TypeFields("UnknownType")
	require.NotNil(t, result["fields"])
	fields := result["fields"].([]any)
	require.Len(t, fields, 1, "unknown type should have only id field")
	fieldMap := fields[0].(map[string]any)
	require.Equal(t, "id", fieldMap["name"])
}

// =============================================================================
// FieldResp Tests
// =============================================================================

// TestFieldResp_SingleField tests FieldResp with single field
func TestFieldResp_SingleField(t *testing.T) {
	result := graphql.FieldResp("id")
	require.NotNil(t, result["fields"])
	fields := result["fields"].([]any)
	require.Len(t, fields, 1)
	fieldMap := fields[0].(map[string]any)
	require.Equal(t, "id", fieldMap["name"])
}

// TestFieldResp_MultipleFields tests FieldResp with multiple fields
func TestFieldResp_MultipleFields(t *testing.T) {
	result := graphql.FieldResp("id", "name", "login")
	fields := result["fields"].([]any)
	require.Len(t, fields, 3)

	names := []string{}
	for _, f := range fields {
		fieldMap := f.(map[string]any)
		names = append(names, fieldMap["name"].(string))
	}

	require.Contains(t, names, "id")
	require.Contains(t, names, "name")
	require.Contains(t, names, "login")
}

// TestFieldResp_NoFields tests FieldResp with no fields
func TestFieldResp_NoFields(t *testing.T) {
	result := graphql.FieldResp()
	fields := result["fields"].([]any)
	require.Len(t, fields, 0, "should return empty fields array")
}

// =============================================================================
// FieldRespWithArgs Tests
// =============================================================================

// TestFieldRespWithArgs_NoArgs tests FieldRespWithArgs with no args
func TestFieldRespWithArgs_NoArgs(t *testing.T) {
	result := graphql.FieldRespWithArgs(nil, "id", "name")
	fields := result["fields"].([]any)
	require.Len(t, fields, 2)

	for _, f := range fields {
		fieldMap := f.(map[string]any)
		_, hasArgs := fieldMap["args"]
		require.False(t, hasArgs, "field should not have args")
	}
}

// TestFieldRespWithArgs_SingleFieldWithArgs tests FieldRespWithArgs with one field having args
func TestFieldRespWithArgs_SingleFieldWithArgs(t *testing.T) {
	argsMap := map[string][]string{
		"items": {"first", "after"},
	}
	result := graphql.FieldRespWithArgs(argsMap, "id", "items", "name")
	fields := result["fields"].([]any)
	require.Len(t, fields, 3)

	// Check items field has args
	itemsField := fields[1].(map[string]any)
	require.Equal(t, "items", itemsField["name"])
	args, hasArgs := itemsField["args"]
	require.True(t, hasArgs, "items should have args")
	argsList := args.([]any)
	require.Len(t, argsList, 2)

	argNames := []string{}
	for _, a := range argsList {
		argMap := a.(map[string]any)
		argNames = append(argNames, argMap["name"].(string))
	}
	require.Contains(t, argNames, "first")
	require.Contains(t, argNames, "after")

	// Check other fields don't have args
	idField := fields[0].(map[string]any)
	_, hasArgs = idField["args"]
	require.False(t, hasArgs, "id should not have args")
}

// TestFieldRespWithArgs_MultipleFieldsWithArgs tests FieldRespWithArgs with multiple fields having args
func TestFieldRespWithArgs_MultipleFieldsWithArgs(t *testing.T) {
	argsMap := map[string][]string{
		"items":  {"first", "after"},
		"fields": {"first"},
	}
	result := graphql.FieldRespWithArgs(argsMap, "id", "items", "fields", "name")
	fields := result["fields"].([]any)
	require.Len(t, fields, 4)

	// Check items field
	itemsField := fields[1].(map[string]any)
	args, hasArgs := itemsField["args"]
	require.True(t, hasArgs)
	require.Len(t, args.([]any), 2)

	// Check fields field
	fieldsField := fields[2].(map[string]any)
	args, hasArgs = fieldsField["args"]
	require.True(t, hasArgs)
	require.Len(t, args.([]any), 1)
}

// =============================================================================
// EnumResp Tests
// =============================================================================

// TestEnumResp_SingleValue tests EnumResp with single value
func TestEnumResp_SingleValue(t *testing.T) {
	result := graphql.EnumResp("OPEN")
	require.NotNil(t, result["enumValues"])
	values := result["enumValues"].([]any)
	require.Len(t, values, 1)
	valueMap := values[0].(map[string]any)
	require.Equal(t, "OPEN", valueMap["name"])
}

// TestEnumResp_MultipleValues tests EnumResp with multiple values
func TestEnumResp_MultipleValues(t *testing.T) {
	result := graphql.EnumResp("OPEN", "CLOSED", "MERGED")
	values := result["enumValues"].([]any)
	require.Len(t, values, 3)

	names := []string{}
	for _, v := range values {
		valueMap := v.(map[string]any)
		names = append(names, valueMap["name"].(string))
	}

	require.Contains(t, names, "OPEN")
	require.Contains(t, names, "CLOSED")
	require.Contains(t, names, "MERGED")
}

// TestEnumResp_NoValues tests EnumResp with no values
func TestEnumResp_NoValues(t *testing.T) {
	result := graphql.EnumResp()
	values := result["enumValues"].([]any)
	require.Len(t, values, 0, "should return empty enumValues array")
}

// TestEnumResp_SearchTypeValues tests EnumResp with SearchType values
func TestEnumResp_SearchTypeValues(t *testing.T) {
	result := graphql.EnumResp("ISSUE", "ISSUE_ADVANCED", "REPOSITORY", "USER", "DISCUSSION")
	values := result["enumValues"].([]any)
	require.Len(t, values, 5)

	valueNames := make(map[string]bool)
	for _, v := range values {
		valueMap := v.(map[string]any)
		valueNames[valueMap["name"].(string)] = true
	}

	require.True(t, valueNames["ISSUE"])
	require.True(t, valueNames["ISSUE_ADVANCED"])
	require.True(t, valueNames["REPOSITORY"])
	require.True(t, valueNames["USER"])
	require.True(t, valueNames["DISCUSSION"])
}
