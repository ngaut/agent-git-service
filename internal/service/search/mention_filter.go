package search

import (
	"context"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/mentions"

	"gorm.io/gorm"
)

func mentionCandidatePattern(mention string) string {
	mention = strings.TrimSpace(mention)
	if mention == "" {
		return ""
	}
	if !strings.HasPrefix(mention, "@") {
		mention = "@" + mention
	}
	return "%" + escapeLike(strings.ToLower(mention)) + "%"
}

func issueMatchesMention(ctx context.Context, baseDB *gorm.DB, issue db.Issue, mention string) (bool, error) {
	if mentions.ContainsLogin(issue.Title, mention) || mentions.ContainsLogin(string(issue.Body), mention) {
		return true, nil
	}
	var comments []string
	if err := baseDB.WithContext(ctx).
		Table("issue_comments").
		Where("repository_id = ? AND issue_number = ?", issue.RepositoryID, issue.Number).
		Pluck("body", &comments).Error; err != nil {
		return false, err
	}
	for _, body := range comments {
		if mentions.ContainsLogin(body, mention) {
			return true, nil
		}
	}
	return false, nil
}
