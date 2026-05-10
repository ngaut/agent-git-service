package service

import (
	"context"
	"sort"
	"time"

	"gh-server/internal/db"
)

// TimelineEvent represents a synthesized event in an issue or PR timeline.
type TimelineEvent struct {
	Event     string    `json:"event"`
	CreatedAt time.Time `json:"created_at"`
	Actor     string    `json:"actor,omitempty"` // login of the user who triggered the event
	// Additional fields are populated dynamically in the transform layer based on Event type
	Comment  *db.IssueComment
	Review   *db.PullRequestReview
	EventRec *db.IssueEvent
}

// GetIssueTimeline synthesizes a timeline of events for an issue or pull request.
// It combines IssueComments, PullRequestReviews, and potentially other events, sorting them chronologically.
func (s *Service) GetIssueTimeline(ctx context.Context, repoFullName string, number int) ([]TimelineEvent, error) {
	var events []TimelineEvent

	// First, fetch the repo to get the repo context
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}

	// Verify it exists as either an issue or PR
	issue, issueErr := s.GetIssue(ctx, repoFullName, number)
	pr, prErr := s.GetPR(ctx, repoFullName, number)
	if issueErr != nil && prErr != nil {
		return nil, wrapErrf(issueErr, "issue or pull request #%d", number)
	}

	// 0. If it's an issue, include persisted issue events.
	if issueErr == nil {
		var issueEvents []db.IssueEvent
		if err := s.DBForCtx(ctx).
			Where("issue_id = ?", issue.ID).
			Order("created_at asc, id asc").
			Find(&issueEvents).Error; err != nil {
			return nil, err
		}
		for i := range issueEvents {
			events = append(events, TimelineEvent{
				Event:     issueEvents[i].EventType,
				CreatedAt: issueEvents[i].CreatedAt,
				Actor:     issueEvents[i].ActorLogin,
				EventRec:  &issueEvents[i],
			})
		}
	}

	// 1. Fetch Comments
	comments, err := s.ListIssueCommentsByRepoID(ctx, rep.ID, number)
	if err != nil {
		return nil, err
	}
	for i := range comments {
		events = append(events, TimelineEvent{
			Event:     "commented",
			CreatedAt: comments[i].CreatedAt,
			Actor:     comments[i].Author.Login,
			Comment:   &comments[i],
		})
	}

	// 2. If it's a PR, fetch reviews
	if prErr == nil {
		reviews, err := s.ListPRReviews(ctx, pr.ID)
		if err == nil {
			for i := range reviews {
				// Don't include pending reviews in the timeline natively if they have no submit date,
				// or use CreatedAt.
				t := reviews[i].CreatedAt
				if reviews[i].SubmittedAt != nil {
					t = *reviews[i].SubmittedAt
				}
				events = append(events, TimelineEvent{
					Event:     "reviewed",
					CreatedAt: t,
					Actor:     reviews[i].AuthorLogin,
					Review:    &reviews[i],
				})
			}
		}
	}

	// Sort all events chronologically
	sort.Slice(events, func(i, j int) bool {
		if events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return timelineSortID(events[i]) < timelineSortID(events[j])
		}
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})

	return events, nil
}

func timelineSortID(e TimelineEvent) uint {
	if e.EventRec != nil {
		return e.EventRec.ID
	}
	if e.Comment != nil {
		return e.Comment.ID
	}
	if e.Review != nil {
		return e.Review.ID
	}
	return 0
}
