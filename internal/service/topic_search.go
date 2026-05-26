package service

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
)

type TopicSearchResult struct {
	Name            string
	RepositoryCount int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type topicSearchQuery struct {
	terms                 []string
	repositories          string
	hasSupportedFilter    bool
	hasUnsupportedFilters bool
}

func parseTopicSearchQuery(query string) topicSearchQuery {
	var sq topicSearchQuery
	for _, token := range strings.Fields(query) {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		inner := token
		negated := false
		if strings.HasPrefix(inner, "-") && len(inner) > 1 && strings.Contains(inner, ":") {
			negated = true
			inner = inner[1:]
		}

		parts := strings.SplitN(inner, ":", 2)
		if len(parts) != 2 {
			if !negated {
				sq.terms = append(sq.terms, strings.ToLower(strings.Trim(token, `"`)))
			}
			continue
		}

		key := strings.ToLower(parts[0])
		value := strings.Trim(parts[1], `"`)
		if negated {
			sq.hasUnsupportedFilters = true
			continue
		}
		switch key {
		case "repositories", "repos":
			sq.repositories = value
			sq.hasSupportedFilter = true
		case "is", "created":
			// GitHub supports topic metadata that local repositories do not model.
			sq.hasUnsupportedFilters = true
		default:
			sq.terms = append(sq.terms, strings.ToLower(strings.Trim(token, `"`)))
		}
	}
	return sq
}

func topicRepositoryPredicate(raw string) (func(int) bool, bool) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		return nil, false
	}
	ops := []string{">=", "<=", ">", "<"}
	for _, op := range ops {
		if strings.HasPrefix(spec, op) {
			n, err := strconv.Atoi(strings.TrimSpace(spec[len(op):]))
			if err != nil {
				return nil, false
			}
			switch op {
			case ">=":
				return func(v int) bool { return v >= n }, true
			case "<=":
				return func(v int) bool { return v <= n }, true
			case ">":
				return func(v int) bool { return v > n }, true
			case "<":
				return func(v int) bool { return v < n }, true
			}
		}
	}
	if strings.HasPrefix(spec, "=") {
		spec = strings.TrimSpace(spec[1:])
	}
	n, err := strconv.Atoi(spec)
	if err != nil {
		return nil, false
	}
	return func(v int) bool { return v == n }, true
}

func topicMatchesTerms(topic string, terms []string) bool {
	for _, term := range terms {
		term = strings.TrimSpace(strings.ToLower(term))
		if term == "" {
			continue
		}
		if !strings.Contains(topic, term) {
			return false
		}
	}
	return true
}

// SearchTopics searches repository topics and returns a GitHub-style topic
// result set. Local topic metadata is repository-derived, so curated/featured
// filters are not broadened when requested.
func (s *Service) SearchTopics(ctx context.Context, query string) ([]TopicSearchResult, error) {
	sq := parseTopicSearchQuery(query)
	if sq.hasUnsupportedFilters {
		return []TopicSearchResult{}, nil
	}
	if len(sq.terms) == 0 && !sq.hasSupportedFilter {
		return []TopicSearchResult{}, nil
	}

	var repos []db.Repository
	if err := s.DBForCtx(ctx).
		Select("id", "topics", "created_at", "updated_at").
		Where("topics <> ''").
		Find(&repos).Error; err != nil {
		return nil, err
	}

	pred, predOK := topicRepositoryPredicate(sq.repositories)
	if sq.repositories != "" && !predOK {
		return []TopicSearchResult{}, nil
	}
	topics := map[string]TopicSearchResult{}
	for _, repo := range repos {
		if err := s.requireRepoPermission(ctx, repo.ID, RepoPermissionRead); err != nil {
			if err == ErrNotFound {
				continue
			}
			return nil, err
		}

		seenInRepo := map[string]struct{}{}
		for _, raw := range strings.Split(repo.Topics, ",") {
			topic := strings.ToLower(strings.TrimSpace(raw))
			if topic == "" {
				continue
			}
			if _, seen := seenInRepo[topic]; seen {
				continue
			}
			seenInRepo[topic] = struct{}{}

			result := topics[topic]
			if result.Name == "" {
				result.Name = topic
				result.CreatedAt = repo.CreatedAt
				result.UpdatedAt = repo.UpdatedAt
			}
			result.RepositoryCount++
			if !repo.CreatedAt.IsZero() && (result.CreatedAt.IsZero() || repo.CreatedAt.Before(result.CreatedAt)) {
				result.CreatedAt = repo.CreatedAt
			}
			if repo.UpdatedAt.After(result.UpdatedAt) {
				result.UpdatedAt = repo.UpdatedAt
			}
			topics[topic] = result
		}
	}

	out := make([]TopicSearchResult, 0, len(topics))
	for _, topic := range topics {
		if !topicMatchesTerms(topic.Name, sq.terms) {
			continue
		}
		if predOK && !pred(topic.RepositoryCount) {
			continue
		}
		out = append(out, topic)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RepositoryCount != out[j].RepositoryCount {
			return out[i].RepositoryCount > out[j].RepositoryCount
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > defaultListLimit {
		out = out[:defaultListLimit]
	}
	return out, nil
}
