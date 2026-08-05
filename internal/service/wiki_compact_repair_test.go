package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestCompactWikiHistory_GitProjectionFailureLeavesRetryablePendingProjection_Issue1472(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-projection-owner", Name: "wiki-compact-projection-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{
		Login: "wiki-bot",
		Name:  "Wiki Bot",
		Email: "gh-server@localhost",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed author user: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-compact-projection",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-compact-projection"

	first, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nFirst version.\n", "create home", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nSecond version.\n", "update home", first.SHA); err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}

	service.SetTestWikiCompactRefUpdateFailureForTest(svc, func(repoFullName, commitSHA string) error {
		return errors.New("synthetic projection failure")
	})

	if _, err := svc.CompactWikiHistory(ctx, full); err == nil {
		t.Fatal("CompactWikiHistory with projection failure succeeded, want error")
	}

	var changesets []db.WikiChangeset
	if err := svc.DB.
		Where("repository_id = (SELECT id FROM repositories WHERE full_name = ?)", full).
		Order("changeset_id ASC").
		Find(&changesets).Error; err != nil {
		t.Fatalf("load changesets: %v", err)
	}
	if len(changesets) != 3 {
		t.Fatalf("changeset count = %d, want 3", len(changesets))
	}
	if changesets[0].SupersededByChangesetID == nil || changesets[1].SupersededByChangesetID == nil {
		t.Fatalf("pre-compact changesets should remain superseded after failed projection: %+v", changesets)
	}
	if changesets[2].SupersededByChangesetID != nil {
		t.Fatalf("compact changeset should remain live after failed projection: %+v", changesets[2])
	}
	if changesets[2].SynthFormatVer != 0 {
		t.Fatalf("compact changeset synth_format_ver = %d, want 0 when projection ref update fails", changesets[2].SynthFormatVer)
	}

	service.SetTestWikiCompactRefUpdateFailureForTest(svc, nil)

	retryResult, err := svc.CompactWikiHistory(ctx, full)
	if err != nil {
		t.Fatalf("CompactWikiHistory(retry pending projection): %v", err)
	}
	if retryResult.NewHead != changesets[2].SynthCommitSHA {
		t.Fatalf("retry NewHead = %q, want existing compact sha %q", retryResult.NewHead, changesets[2].SynthCommitSHA)
	}

	history, err := svc.ListWikiPageHistory(ctx, full, "home")
	if err != nil {
		t.Fatalf("ListWikiPageHistory(after retry): %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len after retry = %d, want 1", len(history))
	}

	var latestChangesets []db.WikiChangeset
	if err := svc.DB.
		Where("repository_id = (SELECT id FROM repositories WHERE full_name = ?)", full).
		Order("changeset_id ASC").
		Find(&latestChangesets).Error; err != nil {
		t.Fatalf("reload changesets: %v", err)
	}
	if len(latestChangesets) != 3 {
		t.Fatalf("changeset count after retry = %d, want 3", len(latestChangesets))
	}
	if latestChangesets[2].SynthFormatVer != 1 {
		t.Fatalf("compact changeset synth_format_ver after retry = %d, want 1", latestChangesets[2].SynthFormatVer)
	}
}

func TestCompactWikiHistoryRecoversInterruptedRESTProjection(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-recover-owner", Name: "Wiki Compact Recover Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{
		Login: "wiki-bot",
		Name:  "Wiki Bot",
		Email: "gh-server@localhost",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed author user: %v", err)
	}
	full := owner.Login + "/wiki-compact-recover"
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-compact-recover",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	publishErr := errors.New("synthetic REST publish failure")
	service.SetTestWikiPreparedPublishFailureForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" {
			return publishErr
		}
		return nil
	})
	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "create home", ""); !errors.Is(err, publishErr) {
		t.Fatalf("PutWikiPage error = %v, want %v", err, publishErr)
	}
	service.SetTestWikiPreparedPublishFailureForTest(svc, nil)

	if _, err := svc.Git.HeadSHA(ctx, full+".wiki", "master"); err == nil {
		t.Fatal("wiki HEAD became visible despite publish failure")
	}

	var prepared db.WikiChangeset
	if err := svc.DB.Where("repository_id = ?", repo.ID).First(&prepared).Error; err != nil {
		t.Fatalf("load prepared changeset: %v", err)
	}
	result, err := svc.CompactWikiHistory(ctx, full)
	if err != nil {
		t.Fatalf("CompactWikiHistory: %v", err)
	}
	if result.PreviousHead != prepared.SynthCommitSHA {
		t.Fatalf("previous head = %s, want recovered REST head %s", result.PreviousHead, prepared.SynthCommitSHA)
	}
	gitHead, err := svc.Git.HeadSHA(ctx, full+".wiki", "master")
	if err != nil {
		t.Fatalf("read recovered wiki HEAD: %v", err)
	}
	if gitHead != prepared.SynthCommitSHA {
		t.Fatalf("wiki HEAD = %s, want recovered REST head %s", gitHead, prepared.SynthCommitSHA)
	}
	body, err := svc.Git.ReadFile(ctx, full+".wiki", "home.md")
	if err != nil {
		t.Fatalf("read recovered page: %v", err)
	}
	if string(body) != "# Home\n" {
		t.Fatalf("recovered page body = %q, want %q", body, "# Home\n")
	}
}

func TestCompactWikiHistoryWaitsForInFlightRESTPublish(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-publish-owner", Name: "Wiki Compact Publish Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{
		Login: "wiki-bot",
		Name:  "Wiki Bot",
		Email: "gh-server@localhost",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed author user: %v", err)
	}
	full := owner.Login + "/wiki-compact-publish"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-compact-publish",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}

	publishStarted := make(chan struct{})
	releasePublish := make(chan struct{})
	var publishOnce sync.Once
	var releaseOnce sync.Once
	service.SetTestWikiPreparedPublishFailureForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" {
			publishOnce.Do(func() {
				close(publishStarted)
				<-releasePublish
			})
		}
		return nil
	})
	defer service.SetTestWikiPreparedPublishFailureForTest(svc, nil)
	defer releaseOnce.Do(func() { close(releasePublish) })

	writeDone := make(chan error, 1)
	go func() {
		_, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "create home", "")
		writeDone <- err
	}()
	select {
	case <-publishStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("REST publish did not start")
	}

	compactDone := make(chan error, 1)
	go func() {
		_, err := svc.CompactWikiHistory(ctx, full)
		compactDone <- err
	}()
	select {
	case err := <-compactDone:
		t.Fatalf("compaction returned before REST publish completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(releasePublish) })
	for name, done := range map[string]<-chan error{
		"write":      writeDone,
		"compaction": compactDone,
	} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s failed: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not finish", name)
		}
	}
}
