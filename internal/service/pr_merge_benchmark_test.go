package service_test

import (
	"context"
	"fmt"
	"testing"
)

func BenchmarkMergePR(b *testing.B) {
	svc, cleanup := setupTestService(b)
	defer cleanup()

	type mergeCase struct {
		ctx      context.Context
		fullName string
		number   int
	}

	cases := make([]mergeCase, b.N)
	for i := 0; i < b.N; i++ {
		login := fmt.Sprintf("bench-merge-user-%d", i)
		repoName := fmt.Sprintf("bench-merge-repo-%d", i)
		pr, authCtx, _ := setupPRWithRealBranches(b, svc, login, repoName)
		cases[i] = mergeCase{
			ctx:      authCtx,
			fullName: login + "/" + repoName,
			number:   pr.Number,
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		merged, err := svc.MergePR(cases[i].ctx, cases[i].fullName, cases[i].number, "merge", "")
		if err != nil {
			b.Fatalf("MergePR(%d): %v", i, err)
		}
		if !merged.Merged || merged.MergeCommitSHA == "" {
			b.Fatalf("MergePR(%d) returned incomplete merge result: %+v", i, merged)
		}
	}
}
