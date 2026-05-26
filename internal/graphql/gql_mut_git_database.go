package graphql

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/service"
)

func (s *Server) loadRepoWithWriteAccess(ctx context.Context, repositoryID string) (db.Repository, map[string]any, bool) {
	repoDBID := parseNodeID(repositoryID, "Repository")
	if repoDBID == 0 {
		return db.Repository{}, errResp("repository not found"), false
	}

	repo, err := s.Svc.GetRepoByID(ctx, fmt.Sprintf("%d", repoDBID))
	if err != nil {
		return db.Repository{}, errResp("repository not found"), false
	}

	u, err := s.Svc.GetCurrentUser(ctx)
	if err != nil {
		return db.Repository{}, errResp("unauthorized"), false
	}

	perm, err := s.Svc.HasRepoAccess(ctx, repo.ID, u.ID)
	if err != nil || !perm.AtLeast(service.RepoPermissionWrite) {
		return db.Repository{}, errResp("repository not found"), false
	}

	return repo, nil, true
}

func gitBlobGQL(blob gitstore.GitBlobObject, raw []byte) map[string]any {
	return map[string]any{
		"oid":        blob.SHA,
		"sha":        blob.SHA,
		"byteSize":   blob.Size,
		"size":       blob.Size,
		"isBinary":   !isLikelyText(raw),
		"__typename": "Blob",
	}
}

func gitTreeEntryGQL(entry gitstore.GitTreeEntry) map[string]any {
	return map[string]any{
		"path":       entry.Path,
		"mode":       entry.Mode,
		"type":       strings.ToUpper(entry.Type),
		"oid":        entry.SHA,
		"sha":        entry.SHA,
		"__typename": "TreeEntry",
	}
}

func gitTreeGQL(tree gitstore.GitTreeObject) map[string]any {
	entries := make([]any, 0, len(tree.Entries))
	for _, entry := range tree.Entries {
		entries = append(entries, gitTreeEntryGQL(entry))
	}
	return map[string]any{
		"oid":        tree.SHA,
		"sha":        tree.SHA,
		"entries":    entries,
		"__typename": "Tree",
	}
}

func isLikelyText(raw []byte) bool {
	for _, b := range raw {
		switch b {
		case '\n', '\r', '\t':
			continue
		}
		if b == 0 || b < 0x20 || b == 0x7f {
			return false
		}
	}
	return true
}

func (s *Server) doCreateBlob(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	repo, errRespData, ok := s.loadRepoWithWriteAccess(ctx, strFrom(inp, "repositoryId"))
	if !ok {
		return errRespData
	}

	content := strFrom(inp, "content")
	encoding := strings.ToUpper(strings.TrimSpace(strFrom(inp, "encoding")))
	if encoding == "" {
		encoding = "UTF8"
	}

	var raw []byte
	switch encoding {
	case "UTF8":
		raw = []byte(content)
	case "BASE64":
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return errResp("invalid base64 content")
		}
		raw = decoded
	default:
		return errResp("unsupported encoding")
	}

	blob, err := s.Svc.Git.CreateBlobObject(ctx, repo.FullName, raw)
	if err != nil {
		return errResp(err.Error())
	}

	return wrap("createBlob", map[string]any{
		"blob": gitBlobGQL(blob, raw),
	})
}

func (s *Server) doCreateTree(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	repo, errRespData, ok := s.loadRepoWithWriteAccess(ctx, strFrom(inp, "repositoryId"))
	if !ok {
		return errRespData
	}

	rawEntries, ok := inp["entries"].([]any)
	if !ok || len(rawEntries) == 0 {
		return errResp("tree must contain at least one entry")
	}

	entries := make([]gitstore.CreateTreeEntryInput, 0, len(rawEntries))
	for _, rawEntry := range rawEntries {
		entryMap, ok := rawEntry.(map[string]any)
		if !ok {
			return errResp("invalid tree entry")
		}

		var content *string
		if v, ok := entryMap["content"].(string); ok {
			content = &v
		}

		sha, deleteSHA, err := graphQLTreeObjectRef(entryMap)
		if err != nil {
			return errResp(err.Error())
		}

		entries = append(entries, gitstore.CreateTreeEntryInput{
			Path:      strFrom(entryMap, "path"),
			Mode:      strFrom(entryMap, "mode"),
			Type:      strings.ToLower(strFrom(entryMap, "type")),
			SHA:       sha,
			Content:   content,
			DeleteSHA: deleteSHA,
		})
	}

	baseTree := strFrom(inp, "baseTreeOid")
	if baseTree == "" {
		baseTree = strFrom(inp, "baseTreeSha")
	}

	tree, err := s.Svc.Git.CreateTreeObject(ctx, repo.FullName, gitstore.CreateTreeOptions{
		BaseTree: strings.TrimSpace(baseTree),
		Entries:  entries,
	})
	if err != nil {
		return errResp(err.Error())
	}

	return wrap("createTree", map[string]any{
		"tree": gitTreeGQL(tree),
	})
}

func graphQLTreeObjectRef(entryMap map[string]any) (sha string, deleteSHA bool, err error) {
	shaValue, shaPresent := entryMap["sha"]
	oidValue, oidPresent := entryMap["oid"]
	if shaPresent && oidPresent {
		return "", false, fmt.Errorf("provide either sha or oid, not both")
	}

	if shaPresent {
		if shaValue == nil {
			return "", true, nil
		}
		sha, ok := shaValue.(string)
		if !ok {
			return "", false, fmt.Errorf("sha must be a string or null")
		}
		return strings.TrimSpace(sha), false, nil
	}

	if oidPresent {
		if oidValue == nil {
			return "", true, nil
		}
		oid, ok := oidValue.(string)
		if !ok {
			return "", false, fmt.Errorf("oid must be a string or null")
		}
		return strings.TrimSpace(oid), false, nil
	}

	return "", false, nil
}
