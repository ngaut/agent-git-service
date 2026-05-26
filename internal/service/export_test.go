package service

import "context"

// IsPublicRepoForTest exposes isPublicRepo to external-package tests. Lives
// in an _test.go file so it never ships outside test binaries.
func IsPublicRepoForTest(s *Service, ctx context.Context, repoID uint) bool {
	return s.isPublicRepo(ctx, repoID)
}

// SetTestWikiCompactRefUpdateFailureForTest installs a test-only hook that can
// force CompactWikiHistory to fail after the catalog transaction commits.
func SetTestWikiCompactRefUpdateFailureForTest(s *Service, fn func(repoFullName, commitSHA string) error) {
	s.testWikiCompactRefUpdateFailure = fn
}
