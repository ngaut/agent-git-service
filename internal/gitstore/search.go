package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	gitLogFieldSep  = "\x1f"
	gitLogRecordSep = "\x1e"
)

// ErrCommitNotFound marks a Git lookup where the requested commit object does
// not exist, distinct from backend/readability failures.
var ErrCommitNotFound = errors.New("commit not found")

// SearchCommitInfo holds a commit found by search along with its repository.
type SearchCommitInfo struct {
	SHA            string
	Message        string
	Author         string
	Email          string
	Date           string
	Committer      string
	CommitterEmail string
	CommitterDate  string
	ParentSHAs     []string
	TreeSHA        string
}

// CommitSearchFilters holds optional filters for commit search.
type CommitSearchFilters struct {
	Author         string
	Committer      string
	AuthorName     string
	AuthorEmail    string
	CommitterName  string
	CommitterEmail string
	Hash           string
	Parent         string
	Tree           string
	AuthorDate     string
	CommitterDate  string
	Merge          *bool
}

// SearchCommits searches commit messages across all branches in a repository.
// If filters are provided, they are applied to filter results.
func (s *Store) SearchCommits(ctx context.Context, fullName, query string, filters *CommitSearchFilters) ([]SearchCommitInfo, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}

	// Build git log command with pre-filters where possible
	args := []string{"-C", dir, "log", "--all", "--max-count=100",
		fmt.Sprintf("--format=%%H%s%%an%s%%ae%s%%aI%s%%s%s%%cn%s%%ce%s%%cI%s%%P%s%%T%s",
			gitLogFieldSep, gitLogFieldSep, gitLogFieldSep, gitLogFieldSep, gitLogFieldSep,
			gitLogFieldSep, gitLogFieldSep, gitLogFieldSep, gitLogFieldSep, gitLogRecordSep)}

	// Add --grep for free text search
	if query != "" {
		args = append(args, "--grep", query, "-i")
	}

	// Add author/committer pre-filters
	if filters != nil {
		if filters.Author != "" {
			args = append(args, "--author", filters.Author)
		}
		if filters.Committer != "" {
			args = append(args, "--committer", filters.Committer)
		}
		if filters.Merge != nil && *filters.Merge {
			args = append(args, "--merges")
		}
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var results []SearchCommitInfo
	for _, record := range strings.Split(string(out), gitLogRecordSep) {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		parts := strings.Split(record, gitLogFieldSep)
		if len(parts) != 10 {
			continue
		}

		commit := SearchCommitInfo{
			SHA:            parts[0],
			Author:         parts[1],
			Email:          parts[2],
			Date:           parts[3],
			Message:        parts[4],
			Committer:      parts[5],
			CommitterEmail: parts[6],
			CommitterDate:  parts[7],
			TreeSHA:        parts[9],
		}
		if parts[8] != "" {
			commit.ParentSHAs = strings.Fields(parts[8])
		}

		// Apply post-filters
		if filters != nil && !matchesCommitFilters(commit, query, filters) {
			continue
		}

		results = append(results, commit)
	}

	// Apply hash/parent/tree pre-filters (must be done after fetching since git log doesn't support them directly)
	if filters != nil {
		if filters.Hash != "" {
			filtered := results[:0]
			for _, c := range results {
				if strings.HasPrefix(strings.ToLower(c.SHA), strings.ToLower(filters.Hash)) {
					filtered = append(filtered, c)
				}
			}
			results = filtered
		}
		if filters.Parent != "" {
			filtered := results[:0]
			for _, c := range results {
				for _, p := range c.ParentSHAs {
					if strings.HasPrefix(strings.ToLower(p), strings.ToLower(filters.Parent)) {
						filtered = append(filtered, c)
						break
					}
				}
			}
			results = filtered
		}
		if filters.Tree != "" {
			filtered := results[:0]
			for _, c := range results {
				if strings.HasPrefix(strings.ToLower(c.TreeSHA), strings.ToLower(filters.Tree)) {
					filtered = append(filtered, c)
				}
			}
			results = filtered
		}
	}

	// Limit results
	if len(results) > 30 {
		results = results[:30]
	}

	return results, nil
}

// matchesCommitFilters checks if a commit matches the given filters.
func matchesCommitFilters(commit SearchCommitInfo, query string, filters *CommitSearchFilters) bool {
	if filters == nil {
		return true
	}

	// Author filter (matches name or email)
	if filters.Author != "" {
		if !strings.Contains(strings.ToLower(commit.Author), strings.ToLower(filters.Author)) &&
			!strings.Contains(strings.ToLower(commit.Email), strings.ToLower(filters.Author)) {
			return false
		}
	}

	// Committer filter (matches name or email)
	if filters.Committer != "" {
		if !strings.Contains(strings.ToLower(commit.Committer), strings.ToLower(filters.Committer)) &&
			!strings.Contains(strings.ToLower(commit.CommitterEmail), strings.ToLower(filters.Committer)) {
			return false
		}
	}

	// Author name filter
	if filters.AuthorName != "" {
		if !strings.Contains(strings.ToLower(commit.Author), strings.ToLower(filters.AuthorName)) {
			return false
		}
	}

	// Author email filter
	if filters.AuthorEmail != "" {
		if !strings.Contains(strings.ToLower(commit.Email), strings.ToLower(filters.AuthorEmail)) {
			return false
		}
	}

	// Committer name filter
	if filters.CommitterName != "" {
		if !strings.Contains(strings.ToLower(commit.Committer), strings.ToLower(filters.CommitterName)) {
			return false
		}
	}

	// Committer email filter
	if filters.CommitterEmail != "" {
		if !strings.Contains(strings.ToLower(commit.CommitterEmail), strings.ToLower(filters.CommitterEmail)) {
			return false
		}
	}

	// Date filters (simplified - just check if date string contains the filter)
	if filters.AuthorDate != "" {
		if !matchesDateFilter(commit.Date, filters.AuthorDate) {
			return false
		}
	}

	if filters.CommitterDate != "" {
		if !matchesDateFilter(commit.CommitterDate, filters.CommitterDate) {
			return false
		}
	}

	// Merge filter (already applied via --merges, but double-check)
	if filters.Merge != nil {
		isMerge := len(commit.ParentSHAs) > 1
		if *filters.Merge != isMerge {
			return false
		}
	}

	return true
}

// matchesDateFilter checks if a date matches a date filter.
// Supports: >=DATE, <=DATE, DATE1..DATE2
func matchesDateFilter(date, filter string) bool {
	if filter == "" {
		return true
	}

	// Handle range: DATE1..DATE2
	if strings.Contains(filter, "..") {
		parts := strings.SplitN(filter, "..", 2)
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		if left != "" && date < left {
			return false
		}
		if right != "" && date > right {
			return false
		}
		return true
	}

	// Handle >=DATE
	if strings.HasPrefix(filter, ">=") {
		return date >= strings.TrimSpace(filter[2:])
	}

	// Handle <=DATE
	if strings.HasPrefix(filter, "<=") {
		return date <= strings.TrimSpace(filter[2:])
	}

	// Handle >DATE
	if strings.HasPrefix(filter, ">") {
		return date > strings.TrimSpace(filter[1:])
	}

	// Handle <DATE
	if strings.HasPrefix(filter, "<") {
		return date < strings.TrimSpace(filter[1:])
	}

	// Exact match or prefix
	return strings.HasPrefix(date, filter)
}

// GetCommit returns details for a single commit by SHA.
func (s *Store) GetCommit(ctx context.Context, fullName, sha string) (*SearchCommitInfo, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	if !IsValidRev(sha) {
		return nil, fmt.Errorf("invalid commit SHA")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "show", "-s",
		"--format=%H|%an|%ae|%aI|%s|%P|%T", sha)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if commitLookupNotFound(string(out)) {
			return nil, fmt.Errorf("%w: %s", ErrCommitNotFound, sha)
		}
		return nil, fmt.Errorf("git show %s: %w", sha, err)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil, fmt.Errorf("%w: %s", ErrCommitNotFound, sha)
	}
	parts := strings.SplitN(line, "|", 7)
	if len(parts) < 5 {
		return nil, fmt.Errorf("invalid commit format")
	}
	commit := &SearchCommitInfo{
		SHA:     parts[0],
		Author:  parts[1],
		Email:   parts[2],
		Date:    parts[3],
		Message: parts[4],
		TreeSHA: parts[6],
	}
	if parts[5] != "" {
		commit.ParentSHAs = strings.Fields(parts[5])
	}
	return commit, nil
}

func commitLookupNotFound(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "bad object") ||
		strings.Contains(lower, "unknown revision or path not in the working tree") ||
		strings.Contains(lower, "unknown revision") ||
		strings.Contains(lower, "ambiguous argument") ||
		strings.Contains(lower, "invalid object name") ||
		strings.Contains(lower, "not a valid commit name") ||
		strings.Contains(lower, "not a valid object name")
}

// ListCommitsOptions holds optional parameters for ListCommits.
type ListCommitsOptions struct {
	Path string // Filter commits to only those that modified this path
}

// LatestCommitsForPaths returns the newest commit touching each requested path.
// The lookup is resolved from a single git log walk so callers can attach
// per-path metadata without spawning one history query per file.
func (s *Store) LatestCommitsForPaths(ctx context.Context, fullName string, paths []string) (map[string]SearchCommitInfo, error) {
	return s.LatestCommitsForPathsAtRef(ctx, fullName, "", paths)
}

// LatestCommitsForPathsAtRef returns the latest visible commit for each path at ref.
// When ref is empty, it resolves HEAD and falls back to the first available
// branch only if HEAD itself is dangling.
func (s *Store) LatestCommitsForPathsAtRef(ctx context.Context, fullName, ref string, paths []string) (map[string]SearchCommitInfo, error) {
	if len(paths) == 0 {
		return map[string]SearchCommitInfo{}, nil
	}
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}

	pathSet := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		pathSet[path] = struct{}{}
	}
	if len(pathSet) == 0 {
		return map[string]SearchCommitInfo{}, nil
	}

	commit, err := s.resolveContentCommit(ctx, dir, ref)
	if err != nil {
		return nil, err
	}

	args := []string{"-C", dir, "log", commit, "--format=commit %H|%an|%ae|%aI", "--name-only", "--"}
	args = append(args, paths...)
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return nil, err
	}

	results := make(map[string]SearchCommitInfo, len(pathSet))
	var current SearchCommitInfo
	for _, raw := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "commit ") {
			parts := strings.SplitN(strings.TrimPrefix(line, "commit "), "|", 4)
			if len(parts) != 4 {
				current = SearchCommitInfo{}
				continue
			}
			current = SearchCommitInfo{
				SHA:    parts[0],
				Author: parts[1],
				Email:  parts[2],
				Date:   parts[3],
			}
			continue
		}
		if current.SHA == "" {
			continue
		}
		if _, ok := pathSet[line]; !ok {
			continue
		}
		if _, seen := results[line]; seen {
			continue
		}
		results[line] = current
		if len(results) == len(pathSet) {
			break
		}
	}
	return results, nil
}

// ListCommits returns recent commits on the default branch of a repository.
// If opts.Path is provided, only commits that modified the specified path are returned.
func (s *Store) ListCommits(ctx context.Context, fullName string, maxCount int, opts *ListCommitsOptions) ([]SearchCommitInfo, error) {
	if maxCount <= 0 {
		maxCount = 30
	}
	return s.listCommits(ctx, fullName, &maxCount, nil, opts)
}

// ListAllCommits returns the full commit history on the default branch of a
// repository. If opts.Path is provided, only commits that modified the
// specified path are returned.
func (s *Store) ListAllCommits(ctx context.Context, fullName string, opts *ListCommitsOptions) ([]SearchCommitInfo, error) {
	return s.listCommits(ctx, fullName, nil, nil, opts)
}

// ListCommitsRange returns commits reachable from head but not from base.
// Results follow git log's natural order (newest first). Callers that replay
// history should reverse the slice before applying commits.
func (s *Store) ListCommitsRange(ctx context.Context, fullName, base, head string, opts *ListCommitsOptions) ([]SearchCommitInfo, error) {
	if !IsValidRev(base) || !IsValidRev(head) {
		return nil, fmt.Errorf("invalid commit range")
	}
	return s.listCommits(ctx, fullName, nil, nil, opts, base+".."+head)
}

// ListCommitsPage returns one page of commits on the default branch.
func (s *Store) ListCommitsPage(ctx context.Context, fullName string, page, perPage int, opts *ListCommitsOptions) ([]SearchCommitInfo, error) {
	if page < 1 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 30
	}
	skip := (page - 1) * perPage
	return s.listCommits(ctx, fullName, &perPage, &skip, opts)
}

// CountCommits returns the total number of commits on the default branch.
// If opts.Path is provided, only commits that modified the specified path are counted.
func (s *Store) CountCommits(ctx context.Context, fullName string, opts *ListCommitsOptions) (int, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return 0, err
	}
	if _, err := os.Stat(dir); err != nil {
		return 0, err
	}
	args := []string{"-C", dir, "rev-list", "--count", "HEAD"}
	if opts != nil && opts.Path != "" {
		args = append(args, "--", opts.Path)
	}
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) listCommits(ctx context.Context, fullName string, maxCount *int, skip *int, opts *ListCommitsOptions, revs ...string) ([]SearchCommitInfo, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	args := []string{"-C", dir, "log",
		fmt.Sprintf("--format=%%H%s%%an%s%%ae%s%%aI%s%%s%s%%cn%s%%ce%s%%cI%s%%P%s",
			gitLogFieldSep, gitLogFieldSep, gitLogFieldSep, gitLogFieldSep, gitLogFieldSep,
			gitLogFieldSep, gitLogFieldSep, gitLogFieldSep, gitLogRecordSep)}
	if maxCount != nil {
		args = append(args, fmt.Sprintf("--max-count=%d", *maxCount))
	}
	if skip != nil && *skip > 0 {
		args = append(args, fmt.Sprintf("--skip=%d", *skip))
	}
	args = append(args, revs...)

	// Add path filter if specified
	if opts != nil && opts.Path != "" {
		args = append(args, "--", opts.Path)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var results []SearchCommitInfo
	for _, record := range strings.Split(string(out), gitLogRecordSep) {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		parts := strings.Split(record, gitLogFieldSep)
		if len(parts) != 9 {
			continue
		}
		parentField := parts[8]
		commit := SearchCommitInfo{
			SHA:            parts[0],
			Author:         parts[1],
			Email:          parts[2],
			Date:           parts[3],
			Message:        parts[4],
			Committer:      parts[5],
			CommitterEmail: parts[6],
			CommitterDate:  parts[7],
		}
		if parentField != "" {
			commit.ParentSHAs = strings.Fields(parentField)
		}
		results = append(results, commit)
	}
	return results, nil
}

// SearchCodeResult holds a file path match found by git grep.
type SearchCodeResult struct {
	Path    string
	Line    string
	Content string
}

// CodeSearchFilters holds optional filters for code search.
type CodeSearchFilters struct {
	Filename   string
	Extensions []string
	Path       string
	Language   string
}

// SearchCode searches file contents in a repository using git grep.
// If filters are provided, they are applied to filter results.
// If withContent is true, matching lines are included in Content field.
// Note: results are capped at 30 matches before filters, so filtered results may be fewer.
// Multiple query terms are combined with AND semantics (all terms must match),
// matching GitHub API behavior for code search.
func (s *Store) SearchCode(ctx context.Context, fullName string, queryTerms []string, filters *CodeSearchFilters, withContent bool) ([]SearchCodeResult, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	// Find the HEAD branch
	branchCmd := exec.CommandContext(ctx, "git", "-C", dir, "for-each-ref", "--format=%(refname:short)", RefsHeadsPrefix, "--count=1")
	branchOut, err := branchCmd.Output()
	if err != nil {
		return nil, err
	}
	branch := strings.TrimSpace(string(branchOut))
	if branch == "" {
		return nil, nil
	}

	// Build git grep command
	// Use -n to get line numbers, -i for case-insensitive
	// Use --and with multiple -e patterns for AND semantics (GitHub API behavior)
	grepArgs := []string{"-C", dir, "grep", "-i", "-n"}
	if !withContent {
		grepArgs = append(grepArgs, "-l") // Only list files
	}
	grepArgs = append(grepArgs, "--max-count=30")

	// Add each query term as a separate -e pattern
	// First pattern doesn't need --and, subsequent patterns use --and for AND semantics
	for i, term := range queryTerms {
		if i > 0 {
			grepArgs = append(grepArgs, "--and")
		}
		grepArgs = append(grepArgs, "-e", term)
	}
	grepArgs = append(grepArgs, branch)

	cmd := exec.CommandContext(ctx, "git", grepArgs...)
	out, _ := cmd.Output() // git grep returns exit 1 when no matches

	trimTreeishPrefix := func(value string) string {
		if branch != "" {
			prefix := branch + ":"
			if strings.HasPrefix(value, prefix) {
				return value[len(prefix):]
			}
		}
		if idx := strings.IndexByte(value, ':'); idx >= 0 {
			return value[idx+1:]
		}
		return value
	}

	var results []SearchCodeResult
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}

		var path string
		var content string
		var lineNumber string

		if withContent {
			// Output format: "branch:path:lineNum:content"
			remainder := trimTreeishPrefix(line)
			lastColon := strings.LastIndexByte(remainder, ':')
			if lastColon < 0 {
				continue
			}
			secondLastColon := strings.LastIndexByte(remainder[:lastColon], ':')
			if secondLastColon < 0 {
				continue
			}
			path = remainder[:secondLastColon]
			lineNumber = remainder[secondLastColon+1 : lastColon]
			content = remainder[lastColon+1:]
		} else {
			// Output format: "branch:path"
			path = trimTreeishPrefix(line)
		}

		// Apply filters
		if filters != nil {
			if !matchesFilters(path, filters) {
				continue
			}
		}

		results = append(results, SearchCodeResult{
			Path:    path,
			Line:    lineNumber,
			Content: content,
		})
	}
	return results, nil
}

// matchesFilters checks if a file path matches the given filters.
func matchesFilters(path string, filters *CodeSearchFilters) bool {
	if filters == nil {
		return true
	}

	normalizedPath := strings.ToLower(filepath.ToSlash(path))

	// Extract filename (basename)
	filename := normalizedPath
	if idx := strings.LastIndex(normalizedPath, "/"); idx >= 0 {
		filename = normalizedPath[idx+1:]
	}

	// Extract extension
	ext := ""
	if idx := strings.LastIndex(filename, "."); idx >= 0 {
		ext = filename[idx:]
	}

	// Apply language filter - check if file extension matches the language
	if filters.Language != "" {
		langExts := getExtensionsForLanguage(filters.Language)
		if len(langExts) == 0 {
			// Unknown language - no matches
			return false
		}
		matched := false
		for _, langExt := range langExts {
			if strings.EqualFold(ext, strings.ToLower(langExt)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Apply filename filter
	if filters.Filename != "" {
		pattern := strings.ToLower(filepath.ToSlash(filters.Filename))
		matched, err := filepath.Match(pattern, filename)
		if err != nil || !matched {
			return false
		}
	}

	// Apply extension filter
	if len(filters.Extensions) > 0 {
		matched := false
		for _, e := range filters.Extensions {
			if strings.EqualFold(ext, strings.ToLower(e)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Apply path filter
	if filters.Path != "" {
		filterPath := strings.ToLower(filepath.ToSlash(filters.Path))
		filterPath = strings.TrimPrefix(filterPath, "./")
		filterPath = strings.TrimPrefix(filterPath, "/")
		if filterPath != "" {
			if !strings.HasPrefix(normalizedPath, filterPath) && !strings.Contains(normalizedPath, "/"+filterPath) {
				return false
			}
		}
	}

	return true
}

// getExtensionsForLanguage returns file extensions for a given language.
// This mirrors the server-side service.GetExtensionsForLanguage function.
func getExtensionsForLanguage(language string) []string {
	languageExtensions := map[string][]string{
		"go":         {".go"},
		"golang":     {".go"},
		"python":     {".py", ".pyw", ".pyi"},
		"py":         {".py", ".pyw", ".pyi"},
		"javascript": {".js", ".jsx", ".mjs", ".cjs"},
		"js":         {".js", ".jsx", ".mjs", ".cjs"},
		"typescript": {".ts", ".tsx", ".mts", ".cts"},
		"ts":         {".ts", ".tsx", ".mts", ".cts"},
		"java":       {".java"},
		"c":          {".c", ".h"},
		"c++":        {".cpp", ".cc", ".cxx", ".hpp", ".hxx"},
		"cpp":        {".cpp", ".cc", ".cxx", ".hpp", ".hxx"},
		"c#":         {".cs"},
		"csharp":     {".cs"},
		"ruby":       {".rb", ".erb", ".rake"},
		"rb":         {".rb", ".erb", ".rake"},
		"rust":       {".rs"},
		"php":        {".php", ".php3", ".php4", ".php5", ".phtml"},
		"swift":      {".swift"},
		"kotlin":     {".kt", ".kts"},
		"scala":      {".scala", ".sc"},
		"html":       {".html", ".htm", ".xhtml"},
		"css":        {".css", ".scss", ".sass", ".less"},
		"shell":      {".sh", ".bash", ".zsh", ".fish"},
		"bash":       {".sh", ".bash"},
		"sql":        {".sql"},
		"yaml":       {".yaml", ".yml"},
		"yml":        {".yaml", ".yml"},
		"json":       {".json"},
		"xml":        {".xml"},
		"markdown":   {".md", ".markdown"},
		"md":         {".md", ".markdown"},
		"dockerfile": {"Dockerfile", ".dockerfile"},
		"makefile":   {"Makefile", "makefile", ".mk"},
		"text":       {".txt"},
		"plaintext":  {".txt"},
	}
	return languageExtensions[strings.ToLower(language)]
}
