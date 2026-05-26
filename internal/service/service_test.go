package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

// setupTestService builds a bare service backed by SQLite + temp gitstore.
// Thin wrapper over testharness.NewService to preserve the existing call-site
// signature across hundreds of service-layer tests.
func setupTestService(t testing.TB) (*service.Service, func()) {
	return testharness.NewService(t, testharness.ServiceConfig{})
}

func mustAddOrgMember(t testing.TB, svc *service.Service, orgID, userID uint, role string) {
	t.Helper()
	if err := svc.AddOrgMember(context.Background(), orgID, userID, role); err != nil {
		t.Fatalf("AddOrgMember(%d,%d,%s): %v", orgID, userID, role, err)
	}
}

// setupRepoForTest creates a user and repo for use in service tests.
func setupRepoForTest(t testing.TB, svc *service.Service, login, repoName string) {
	t.Helper()
	svc.DB.Create(&db.User{Login: login, Name: login, Type: db.TypeUser})
	_, err := svc.CreateRepo(context.Background(), service.CreateRepoInput{OwnerLogin: login, Name: repoName})
	if err != nil {
		t.Fatalf("setupRepoForTest failed: %v", err)
	}
}
