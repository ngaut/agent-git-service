package transform

import (
	"encoding/base64"
	"fmt"

	"gh-server/internal/gitstore"
)

func gitVerification() map[string]any {
	return map[string]any{
		"verified":    false,
		"reason":      "unsigned",
		"signature":   nil,
		"payload":     nil,
		"verified_at": nil,
	}
}

// GitCommit converts a low-level git commit object into GitHub-compatible JSON.
func GitCommit(repoFullName string, commit gitstore.GitCommitObject) map[string]any {
	parents := make([]any, 0, len(commit.ParentSHAs))
	for _, parentSHA := range commit.ParentSHAs {
		parents = append(parents, map[string]any{
			"sha":      parentSHA,
			"url":      fmt.Sprintf("%s/api/v3/repos/%s/git/commits/%s", base(), repoFullName, parentSHA),
			"html_url": fmt.Sprintf("%s/%s/commit/%s", htmlBase(), repoFullName, parentSHA),
		})
	}

	return map[string]any{
		"sha":      commit.SHA,
		"node_id":  NodeID("Commit", commit.SHA),
		"url":      fmt.Sprintf("%s/api/v3/repos/%s/git/commits/%s", base(), repoFullName, commit.SHA),
		"html_url": fmt.Sprintf("%s/%s/commit/%s", htmlBase(), repoFullName, commit.SHA),
		"author": map[string]any{
			"name":  commit.Author.Name,
			"email": commit.Author.Email,
			"date":  commit.Author.Date,
		},
		"committer": map[string]any{
			"name":  commit.Committer.Name,
			"email": commit.Committer.Email,
			"date":  commit.Committer.Date,
		},
		"tree": map[string]any{
			"sha": commit.TreeSHA,
			"url": fmt.Sprintf("%s/api/v3/repos/%s/git/trees/%s", base(), repoFullName, commit.TreeSHA),
		},
		"message":      commit.Message,
		"parents":      parents,
		"verification": gitVerification(),
	}
}

// GitBlob converts a low-level git blob object into GitHub-compatible JSON.
// Content is base64-encoded per the GitHub REST contract for GET /git/blobs/{sha}.
func GitBlob(repoFullName string, blob gitstore.GitBlobObject) map[string]any {
	return map[string]any{
		"sha":      blob.SHA,
		"node_id":  NodeID("Blob", blob.SHA),
		"size":     blob.Size,
		"url":      fmt.Sprintf("%s/api/v3/repos/%s/git/blobs/%s", base(), repoFullName, blob.SHA),
		"content":  base64.StdEncoding.EncodeToString(blob.Content),
		"encoding": "base64",
	}
}

// GitTag converts a low-level annotated tag object into GitHub-compatible JSON.
func GitTag(repoFullName string, tag gitstore.GitTagObject) map[string]any {
	return map[string]any{
		"node_id": NodeID("Tag", tag.SHA),
		"sha":     tag.SHA,
		"url":     fmt.Sprintf("%s/api/v3/repos/%s/git/tags/%s", base(), repoFullName, tag.SHA),
		"tagger": map[string]any{
			"name":  tag.Tagger.Name,
			"email": tag.Tagger.Email,
			"date":  tag.Tagger.Date,
		},
		"object": map[string]any{
			"sha":  tag.ObjectSHA,
			"type": tag.ObjectType,
			"url":  gitObjectURL(repoFullName, tag.ObjectType, tag.ObjectSHA),
		},
		"tag":          tag.Tag,
		"message":      tag.Message,
		"verification": gitVerification(),
	}
}

func gitObjectURL(repoFullName, objectType, sha string) any {
	switch objectType {
	case "blob":
		return fmt.Sprintf("%s/api/v3/repos/%s/git/blobs/%s", base(), repoFullName, sha)
	case "tree":
		return fmt.Sprintf("%s/api/v3/repos/%s/git/trees/%s", base(), repoFullName, sha)
	case "commit":
		return fmt.Sprintf("%s/api/v3/repos/%s/git/commits/%s", base(), repoFullName, sha)
	case "tag":
		return fmt.Sprintf("%s/api/v3/repos/%s/git/tags/%s", base(), repoFullName, sha)
	default:
		return nil
	}
}

// GitTree converts a low-level git tree object into GitHub-compatible JSON.
func GitTree(repoFullName string, tree gitstore.GitTreeObject) map[string]any {
	items := make([]any, 0, len(tree.Entries))
	for _, entry := range tree.Entries {
		item := map[string]any{
			"path": entry.Path,
			"mode": entry.Mode,
			"type": entry.Type,
			"sha":  entry.SHA,
		}
		switch entry.Type {
		case "blob":
			item["url"] = fmt.Sprintf("%s/api/v3/repos/%s/git/blobs/%s", base(), repoFullName, entry.SHA)
			if entry.Size != nil {
				item["size"] = *entry.Size
			}
		case "tree":
			item["url"] = fmt.Sprintf("%s/api/v3/repos/%s/git/trees/%s", base(), repoFullName, entry.SHA)
		case "commit":
			item["url"] = fmt.Sprintf("%s/api/v3/repos/%s/git/commits/%s", base(), repoFullName, entry.SHA)
		default:
			item["url"] = nil
		}
		items = append(items, item)
	}

	return map[string]any{
		"sha":       tree.SHA,
		"url":       fmt.Sprintf("%s/api/v3/repos/%s/git/trees/%s", base(), repoFullName, tree.SHA),
		"tree":      items,
		"truncated": tree.Truncated,
	}
}
