# Git Store — Component Reference

## Purpose

`internal/gitstore` manages bare Git repositories on the local filesystem.
It provides the low-level operations that the rest of the system uses for repository lifecycle, refs, merge/rebase, diff, content access, search, and archive streaming.

`gitstore` is infrastructure, not business policy.
It does not make database decisions or shape HTTP responses.

For the full system overview see [docs/architecture.md](../architecture.md).
Core invariant: `agent-git-service` is Git-backed; see [architecture.md § Purpose](../architecture.md#purpose).

## Scope

Owns:

- repository existence, init, fork, delete
- ref and branch management
- merge, rebase, squash, revert operations
- compare, diff, and content read/write
- commit and code search
- archive streaming (zip, tar.gz)
- per-repository write serialization

Does not own:

- business rules around when to merge, who may push, or what happens after a push (that belongs to `service` and `githttp`)
- relational metadata such as PR records, issues, or users (that belongs to `db`)
- HTTP transport or response formatting

## Key Entry Points

| File | Responsibility |
|---|---|
| `store.go` | `Store` struct, constructor (`New`), per-repo mutex via `sync.Map`, repo path helpers, go-git `open` and `SetupConfig` |
| `init.go` | `Init` (bare repo creation with optional README seed), `Fork` (directory copy), `Delete` |
| `refs.go` | `HeadSHA`, `CreateBranch`, `CreateBranchFromOid`, `CreatePRRef`, `UpdateRef`, `DeleteRef`, `ListBranches` |
| `merge.go` | `Merge`, `SquashMerge`, `Rebase`, `RevertMerge`, `UpdatePRBranch`, `SimulateMerge`, `CanMerge`, `IsEmpty`, `DiskUsageKB` |
| `content.go` | `ReadFile`, `WriteFile`, `DeleteFileFromRepo`, `ListTreeFiles`, `ListTags`, `DiffNameStatus`, `DiffNumStat`, `DiffRaw`, `GetDiffHunk` |
| `commit_files.go` | direct blob/tree/commit object writes, prepared-commit CAS publication, and the bounded published-tree cache used by Wiki writes |
| `compare.go` | `Compare` (concurrent 4-goroutine diff), `Contributors`, `LogBetweenTags`, `PRCommitsLog` |
| `search.go` | `SearchCommits`, `ListCommits`, `SearchCode` |
| `archive.go` | `Archive` (stream zip/tar.gz to `io.Writer`) |
| `rev_validator.go` | `IsValidRev` (input validation to prevent command injection) |

## Main Flows

### Repository Lifecycle

```
Init(fullName, seed)
  → go-git: create bare repo at GIT_REPO_DIR/{storage-key}.git
  → if seed: create initial commit with empty README.md via go-git object APIs

Fork(src, dst)
  → cp -a src.git dst.git

Delete(fullName)
  → os.RemoveAll(repoPath)
```

### Merge (Regular)

```
Merge(fullName, base, head, message, author)
  → repoLock(fullName).Lock()           // per-repo mutex
  → cloneToTmp(repoPath)                // temporary clone for safe manipulation
  → git checkout base
  → git merge head --no-ff -m message
  → git push origin base                // push result back to bare repo
  → cleanup tmp dir
  → repoLock(fullName).Unlock()
```

Squash and rebase follow the same lock → clone → operate → push → unlock pattern.
`SimulateMerge` uses `git merge-tree` and `git commit-tree` without altering any branch.

### Compare (Concurrent)

```
Compare(fullName, base, head)
  → launch 4 goroutines via sync.WaitGroup:
      1. git rev-list base..head --count   (ahead)
      2. git rev-list head..base --count   (behind)
      3. git log base..head                (commits)
      4. git diff --numstat base...head     (files)
  → wait, return combined result
```

### Content Write

```
WriteFile(fullName, branch, path, content, message, author)
  → git hash-object -w --stdin           (store blob)
  → git mktree                           (build tree with new blob)
  → git commit-tree -p parent -m message (create commit)
  → git update-ref refs/heads/branch sha (advance branch)
```

Wiki catalog writes use a two-phase object/ref flow without spawning Git
subprocesses:

```
BuildCommitFilesAt(fullName, branch, mutations)
  -> read the current branch parent
  -> encode blob, tree, and commit objects in memory
  -> return PreparedCommit(SHA, ParentSHA) without changing the branch

PersistPreparedCommit(prepared)
  -> write the prepared objects through go-git

PublishPreparedCommit(fullName, branch, prepared)
  -> verify the commit object and parent
  -> CheckAndSetReference(parent -> prepared SHA)
  -> retain the published mutable tree in a bounded cache
```

`PrepareCommitFilesAt` remains the synchronous build-plus-persist wrapper. The
three-stage API lets the Wiki service overlap object persistence with its
catalog transaction after the commit SHA is known. The catalog transaction
uses a pre-commit durability barrier, and the ref remains invisible until both
durable operations succeed. The cache is keyed by repository and branch,
contains at most 16 published trees, transfers ownership to one preparer, and
is discarded whenever the observed branch parent differs. External pushes
therefore invalidate cached state instead of being overwritten.

## Invariants and Design Constraints

- **Per-repository write serialization.** Write-heavy operations (`Merge`, `SquashMerge`, `Rebase`, `RevertMerge`, `UpdatePRBranch`) acquire a per-repo `sync.Mutex` from a `sync.Map` in `Store`. This prevents concurrent modifications to the same bare repository.
- **Prepared refs publish with CAS.** Object creation does not make a commit visible. `PublishPreparedCommit` advances a branch only when it still points at the prepared parent and returns `ErrRefChanged` otherwise.
- **Tree reuse is bounded and parent-validated.** The direct multi-file writer keeps only a small process-local set of published trees. Taking an entry removes it from the cache, so concurrent prepares cannot mutate the same tree; a ref changed by another process or Git push forces a disk reload.
- **Temporary clones for merge safety.** Merge and rebase operations clone to a temp directory, operate there, and push results back. This keeps the bare repo in a consistent state even if the operation fails midway.
- **Hybrid go-git and CLI git usage.** go-git is used for initialization, repo opening, config, and some ref operations. CLI git (via `os/exec`) handles merge, rebase, diff, log, content, archive, and search. The split exists because go-git lacks some plumbing operations.
- **Revision validation.** `IsValidRev` rejects empty strings, leading dashes, and whitespace to prevent command injection in git CLI calls.
- **Bare-repo layout.** Git store manages standard bare Git repositories rooted at `GIT_REPO_DIR/{owner}/{repo}.git`.

For the full dependency-boundary rules see [module-contracts.md § gitstore](../module-contracts.md#gitstore).

## Extension and Change Guidance

**Adding a new git operation:**

1. Add a method to `Store` in the appropriate file (content ops in `content.go`, ref ops in `refs.go`, etc.).
2. If the operation writes to the repository, acquire the per-repo lock via `s.repoLock(fullName)`.
3. For worktree-style merges and rebases, use the clone-to-tmp pattern from `merge.go`. For plumbing-style file commits, use the prepared object/ref flow from `commit_files.go`.
4. Validate any user-supplied revision strings with `IsValidRev` before passing to CLI commands.
5. If the operation is called from `service`, add it to the corresponding service method; gitstore should not call service.

**Common patterns:**

- CLI git invocations use `exec.CommandContext` with the repo path as the working directory.
- go-git operations open the repo with `s.open(fullName)`.
- Error returns are plain `fmt.Errorf` wraps; gitstore does not use the service sentinel errors.

## Related Tests

- `internal/gitstore/store_test.go` — covers init, fork, delete, merge, rebase, squash, conflict handling, and compare
- `internal/gitstore/stubs_test.go` — covers `GetDiffHunk`, `Archive`, and `SimulateMerge`

For the phased test roadmap see [docs/test-strategy.md](../test-strategy.md).

## Related Docs

- [docs/architecture.md](../architecture.md) — system overview
- [docs/module-contracts.md](../module-contracts.md) § gitstore — dependency rules and ownership
- [Service Layer](service.md) — coordinates Git and DB state through gitstore
- [Git Smart HTTP](git-http.md) — uses gitstore directly for transport
