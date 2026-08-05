package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	applog "github.com/ngaut/agent-git-service/internal/logging"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

type wikiPostCommitContextKey struct{}

type wikiPostCommitWaiter struct {
	repo                db.Repository
	catalogChangesetID  uint64
	preparedCommit      gitstore.PreparedCommit
	preparedPersistence *wikiPreparedCommitPersistence
	waits               []<-chan error
}

type wikiPreparedCommitPersistence struct {
	done <-chan error
	once sync.Once
	err  error
}

func withWikiPostCommitWaiter(
	ctx context.Context,
	repo db.Repository,
	prepared gitstore.PreparedCommit,
	persistence <-chan error,
) (context.Context, *wikiPostCommitWaiter) {
	waiter := &wikiPostCommitWaiter{
		repo:                repo,
		preparedCommit:      prepared,
		preparedPersistence: &wikiPreparedCommitPersistence{done: persistence},
	}
	return context.WithValue(ctx, wikiPostCommitContextKey{}, waiter), waiter
}

func wikiPostCommitWaiterFromContext(ctx context.Context, repoID uint) (*wikiPostCommitWaiter, bool) {
	waiter, ok := ctx.Value(wikiPostCommitContextKey{}).(*wikiPostCommitWaiter)
	return waiter, ok && waiter != nil && waiter.repo.ID == repoID
}

func (w *wikiPostCommitWaiter) add(done <-chan error) {
	if w != nil && done != nil {
		w.waits = append(w.waits, done)
	}
}

func (w *wikiPostCommitWaiter) waitPrepared() error {
	if w == nil || w.preparedPersistence == nil || w.preparedPersistence.done == nil {
		return nil
	}
	w.preparedPersistence.once.Do(func() {
		w.preparedPersistence.err = <-w.preparedPersistence.done
	})
	return w.preparedPersistence.err
}

func (w *wikiPostCommitWaiter) wait() error {
	if w == nil {
		return nil
	}
	var waitErr error
	for _, done := range w.waits {
		if err := <-done; err != nil && waitErr == nil {
			waitErr = err
		}
	}
	return waitErr
}

func (s *Service) detachWikiBackgroundContext(ctx context.Context) context.Context {
	detached := applog.CloneContext(s.ServerCtx(), ctx)
	if scopedDB, ok := DBFromContext(ctx); ok {
		detached = ContextWithDB(detached, scopedDB)
	}
	return detached
}

type wikiReferenceJob struct {
	ctx        context.Context
	repo       string
	enqueuedAt time.Time
	run        func(context.Context) error
	after      func(context.Context) error
	done       chan error
}

type wikiReferenceQueue struct {
	mu      sync.Mutex
	jobs    []wikiReferenceJob
	running bool
}

func (s *Service) getWikiReferenceQueue(ctx context.Context, repoFullName string) (*wikiReferenceQueue, error) {
	key := s.scopedRepoMutexKey(s.wikiRepoKey(ctx, db.Repository{FullName: repoFullName}))

	s.wikiReferenceQueuesMu.Lock()
	defer s.wikiReferenceQueuesMu.Unlock()
	if s.wikiReferenceQueues == nil {
		s.wikiReferenceQueues = make(map[string]*wikiReferenceQueue)
	}
	queue := s.wikiReferenceQueues[key]
	if queue == nil {
		queue = &wikiReferenceQueue{}
		s.wikiReferenceQueues[key] = queue
	}
	return queue, nil
}

func (s *Service) enqueueWikiReferenceJob(job wikiReferenceJob) (<-chan error, error) {
	queue, err := s.getWikiReferenceQueue(job.ctx, job.repo)
	if err != nil {
		return nil, err
	}
	job.done = make(chan error, 1)
	job.enqueuedAt = time.Now()

	s.Wg.Add(1)
	queue.mu.Lock()
	queue.jobs = append(queue.jobs, job)
	recordWikiWriteValue(job.ctx, wikiWriteValueReferenceQueueDepth, int64(len(queue.jobs)))
	if queue.running {
		queue.mu.Unlock()
		return job.done, nil
	}
	queue.running = true
	queue.mu.Unlock()

	go s.runWikiReferenceQueue(queue)
	return job.done, nil
}

func (s *Service) runWikiReferenceQueue(queue *wikiReferenceQueue) {
	for {
		queue.mu.Lock()
		if len(queue.jobs) == 0 {
			queue.running = false
			queue.jobs = nil
			queue.mu.Unlock()
			return
		}
		job := queue.jobs[0]
		queue.jobs[0] = wikiReferenceJob{}
		queue.jobs = queue.jobs[1:]
		queue.mu.Unlock()

		recordWikiWriteDuration(job.ctx, wikiWritePhaseReferenceQueueWait, time.Since(job.enqueuedAt))
		err := job.run(job.ctx)
		if err != nil {
			slog.WarnContext(job.ctx, "wiki reference sync failed",
				"repo", job.repo,
				"error", err,
			)
		}
		job.done <- err
		close(job.done)
		if err == nil && job.after != nil {
			if afterErr := job.after(job.ctx); afterErr != nil {
				slog.WarnContext(job.ctx, "wiki reference recovery marker update failed",
					"repo", job.repo,
					"error", afterErr,
				)
			}
		}
		s.Wg.Done()
	}
}

func (s *Service) enqueueWikiPostCommitEffects(
	ctx context.Context,
	repo db.Repository,
	result wikicatalog.ChangeSetResult,
) (<-chan error, error) {
	detached := s.detachWikiBackgroundContext(ctx)
	detached = cloneWikiWriteTiming(detached, ctx)
	job := wikiReferenceJob{
		ctx:  detached,
		repo: repo.FullName,
		run: func(jobCtx context.Context) error {
			if err := s.applyWikiPostCommitEffects(jobCtx, repo, result); err != nil {
				return fmt.Errorf("wiki post-commit effects for %s: %w", repo.FullName, err)
			}
			return nil
		},
	}
	if result.ReferenceEffectsPending {
		if !result.ReferenceEffectsCoalesced {
			job.after = func(jobCtx context.Context) error {
				return s.markWikiReferenceEffectsComplete(jobCtx, repo.ID, result.ChangesetID)
			}
		}
	}
	return s.enqueueWikiReferenceJob(job)
}
