package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const gitTreeMaxEntries = 100000

// GitSignature describes the author or committer stored on a git object.
type GitSignature struct {
	Name  string
	Email string
	Date  string
}

// GitCommitObject is the low-level git database commit representation.
type GitCommitObject struct {
	SHA        string
	TreeSHA    string
	Message    string
	ParentSHAs []string
	Author     GitSignature
	Committer  GitSignature
	Signature  string
	Payload    string
}

// GitTagObject is the low-level annotated tag representation.
type GitTagObject struct {
	SHA        string
	Tag        string
	Message    string
	ObjectSHA  string
	ObjectType string
	Tagger     GitSignature
	Signature  string
	Payload    string
}

// CreateCommitOptions configures low-level commit creation.
type CreateCommitOptions struct {
	Message    string
	TreeSHA    string
	ParentSHAs []string
	Author     GitSignature
	Committer  GitSignature
	Signature  string
}

// CreateTagOptions configures low-level annotated tag creation.
type CreateTagOptions struct {
	Tag       string
	Message   string
	ObjectSHA string
	Type      string
	Tagger    GitSignature
}

// GitTreeObject is the low-level git database tree representation.
type GitTreeObject struct {
	SHA       string
	Entries   []GitTreeEntry
	Truncated bool
}

// GitTreeEntry is a single tree entry returned by ls-tree.
type GitTreeEntry struct {
	Path string
	Mode string
	Type string
	SHA  string
	Size *int64
}

// ErrNotTag is returned by GetGitTagObject when the requested SHA exists but
// is not an annotated tag object.
var ErrNotTag = errors.New("object is not a tag")

// GetGitCommitObject reads a low-level commit object by revision.
func (s *Store) GetGitCommitObject(ctx context.Context, fullName, rev string) (GitCommitObject, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return GitCommitObject{}, err
	}
	if _, err := os.Stat(dir); err != nil {
		return GitCommitObject{}, err
	}

	commitSHA, err := s.resolveCommitSHA(ctx, dir, rev)
	if err != nil {
		return GitCommitObject{}, err
	}
	raw, err := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "commit", commitSHA).Output()
	if err != nil {
		return GitCommitObject{}, fmt.Errorf("git cat-file commit failed: %w", err)
	}
	commit, err := parseRawCommitObject(commitSHA, string(raw))
	if err != nil {
		return GitCommitObject{}, err
	}
	return commit, nil
}

// CreateCommitObject writes a low-level commit object without updating any ref.
func (s *Store) CreateCommitObject(ctx context.Context, fullName string, opts CreateCommitOptions) (GitCommitObject, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return GitCommitObject{}, err
	}
	if _, err := os.Stat(dir); err != nil {
		return GitCommitObject{}, err
	}

	if strings.TrimSpace(opts.Message) == "" {
		return GitCommitObject{}, fmt.Errorf("message is required")
	}
	if strings.TrimSpace(opts.TreeSHA) == "" {
		return GitCommitObject{}, fmt.Errorf("tree is required")
	}

	treeSHA, err := s.resolveTreeSHA(ctx, dir, opts.TreeSHA)
	if err != nil {
		return GitCommitObject{}, err
	}

	parentSHAs := make([]string, 0, len(opts.ParentSHAs))
	for _, parent := range opts.ParentSHAs {
		parent = strings.TrimSpace(parent)
		if parent == "" {
			continue
		}
		parentSHA, err := s.resolveCommitSHA(ctx, dir, parent)
		if err != nil {
			return GitCommitObject{}, err
		}
		parentSHAs = append(parentSHAs, parentSHA)
	}

	author := normalizeGitSignature(opts.Author, GitSignature{
		Name:  defaultCommitName,
		Email: defaultCommitEmail,
		Date:  time.Now().UTC().Format(time.RFC3339),
	})
	committer := normalizeGitSignature(opts.Committer, author)
	payload, err := buildCommitPayload(treeSHA, parentSHAs, author, committer, opts.Message, opts.Signature)
	if err != nil {
		return GitCommitObject{}, err
	}

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Stdin = strings.NewReader(payload)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return GitCommitObject{}, fmt.Errorf("hash-object commit failed: %w", err)
	}

	commitSHA := strings.TrimSpace(string(out))
	if commitSHA == "" {
		return GitCommitObject{}, fmt.Errorf("hash-object commit returned empty sha")
	}

	return s.GetGitCommitObject(ctx, fullName, commitSHA)
}

// GetGitTagObject reads an annotated tag object by SHA.
func (s *Store) GetGitTagObject(ctx context.Context, fullName, rev string) (GitTagObject, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return GitTagObject{}, err
	}
	if _, err := os.Stat(dir); err != nil {
		return GitTagObject{}, err
	}

	tagSHA, err := s.resolveObjectSHA(ctx, dir, rev)
	if err != nil {
		return GitTagObject{}, err
	}

	typeOut, err := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "-t", tagSHA).Output()
	if err != nil {
		return GitTagObject{}, fmt.Errorf("cat-file -t failed: %w", err)
	}
	if t := strings.TrimSpace(string(typeOut)); t != "tag" {
		return GitTagObject{}, fmt.Errorf("%w: %s", ErrNotTag, t)
	}

	raw, err := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "tag", tagSHA).Output()
	if err != nil {
		return GitTagObject{}, fmt.Errorf("git cat-file tag failed: %w", err)
	}
	tag, err := parseRawTagObject(tagSHA, string(raw))
	if err != nil {
		return GitTagObject{}, err
	}
	return tag, nil
}

// CreateTagObject writes an annotated tag object without updating any ref.
func (s *Store) CreateTagObject(ctx context.Context, fullName string, opts CreateTagOptions) (GitTagObject, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return GitTagObject{}, err
	}
	if _, err := os.Stat(dir); err != nil {
		return GitTagObject{}, err
	}

	tagName := strings.TrimSpace(opts.Tag)
	if tagName == "" {
		return GitTagObject{}, fmt.Errorf("tag is required")
	}
	if strings.ContainsAny(tagName, "\x00\n") {
		return GitTagObject{}, fmt.Errorf("tag is invalid")
	}
	message := strings.TrimSpace(opts.Message)
	if message == "" {
		return GitTagObject{}, fmt.Errorf("message is required")
	}
	objectRev := strings.TrimSpace(opts.ObjectSHA)
	if objectRev == "" {
		return GitTagObject{}, fmt.Errorf("object is required")
	}
	objectSHA, err := s.resolveObjectSHA(ctx, dir, objectRev)
	if err != nil {
		return GitTagObject{}, err
	}

	objectType := strings.TrimSpace(opts.Type)
	if objectType == "" {
		return GitTagObject{}, fmt.Errorf("type is required")
	}
	if !validGitTagObjectType(objectType) {
		return GitTagObject{}, fmt.Errorf("unsupported object type %q", objectType)
	}
	typeOut, err := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "-t", objectSHA).Output()
	if err != nil {
		return GitTagObject{}, fmt.Errorf("cat-file -t failed: %w", err)
	}
	if actual := strings.TrimSpace(string(typeOut)); actual != objectType {
		return GitTagObject{}, fmt.Errorf("object type mismatch: got %s, want %s", actual, objectType)
	}

	tagger := normalizeGitSignature(opts.Tagger, GitSignature{
		Name:  defaultCommitName,
		Email: defaultCommitEmail,
		Date:  time.Now().UTC().Format(time.RFC3339),
	})
	payload, err := buildTagPayload(tagName, objectSHA, objectType, tagger, message)
	if err != nil {
		return GitTagObject{}, err
	}

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "hash-object", "-t", "tag", "-w", "--stdin")
	cmd.Stdin = strings.NewReader(payload)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return GitTagObject{}, fmt.Errorf("hash-object tag failed: %w, output: %s", err, out)
	}
	tagSHA := strings.TrimSpace(string(out))
	if tagSHA == "" {
		return GitTagObject{}, fmt.Errorf("hash-object tag returned empty sha")
	}

	return s.GetGitTagObject(ctx, fullName, tagSHA)
}

// GetGitTree reads a git tree by tree-ish or commit-ish revision.
func (s *Store) GetGitTree(ctx context.Context, fullName, rev string, recursive bool) (GitTreeObject, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return GitTreeObject{}, err
	}
	if _, err := os.Stat(dir); err != nil {
		return GitTreeObject{}, err
	}

	objectSHA, err := s.resolveObjectSHA(ctx, dir, rev)
	if err != nil {
		return GitTreeObject{}, err
	}
	treeSHA, err := s.resolveTreeSHA(ctx, dir, rev)
	if err != nil {
		return GitTreeObject{}, err
	}

	args := []string{"-C", dir, "ls-tree", "-z", "-l"}
	if recursive {
		args = append(args, "-r", "-t")
	}
	args = append(args, treeSHA)

	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return GitTreeObject{}, fmt.Errorf("git ls-tree failed: %w", err)
	}

	entries, err := parseGitTreeEntries(string(out))
	if err != nil {
		return GitTreeObject{}, err
	}

	truncated := false
	if recursive && len(entries) > gitTreeMaxEntries {
		entries = entries[:gitTreeMaxEntries]
		truncated = true
	}

	return GitTreeObject{SHA: objectSHA, Entries: entries, Truncated: truncated}, nil
}

func normalizeGitSignature(sig, fallback GitSignature) GitSignature {
	out := fallback
	if name := strings.TrimSpace(sig.Name); name != "" {
		out.Name = name
	}
	if email := strings.TrimSpace(sig.Email); email != "" {
		out.Email = email
	}
	if date := strings.TrimSpace(sig.Date); date != "" {
		out.Date = date
	}
	if out.Name == "" {
		out.Name = defaultCommitName
	}
	if out.Email == "" {
		out.Email = defaultCommitEmail
	}
	if out.Date == "" {
		out.Date = time.Now().UTC().Format(time.RFC3339)
	}
	return out
}

func (s *Store) resolveCommitSHA(ctx context.Context, dir, rev string) (string, error) {
	if !IsValidRev(rev) {
		return "", fmt.Errorf("invalid commit revision")
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", rev+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("commit not found: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *Store) resolveObjectSHA(ctx context.Context, dir, rev string) (string, error) {
	if !IsValidRev(rev) {
		return "", fmt.Errorf("invalid object revision")
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", rev).Output()
	if err != nil {
		return "", fmt.Errorf("object not found: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *Store) resolveTreeSHA(ctx context.Context, dir, rev string) (string, error) {
	if !IsValidRev(rev) {
		return "", fmt.Errorf("invalid tree revision")
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", rev+"^{tree}").Output()
	if err != nil {
		return "", fmt.Errorf("tree not found: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func parseGitTreeEntries(out string) ([]GitTreeEntry, error) {
	if out == "" {
		return nil, nil
	}

	rawEntries := strings.Split(out, "\x00")
	entries := make([]GitTreeEntry, 0, len(rawEntries))
	for _, rawEntry := range rawEntries {
		if rawEntry == "" {
			continue
		}
		parts := strings.SplitN(rawEntry, "\t", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid ls-tree entry")
		}

		meta := strings.Fields(parts[0])
		if len(meta) < 4 {
			return nil, fmt.Errorf("invalid ls-tree metadata")
		}

		entry := GitTreeEntry{
			Path: parts[1],
			Mode: meta[0],
			Type: meta[1],
			SHA:  meta[2],
		}
		if meta[3] != "-" {
			size, err := strconv.ParseInt(meta[3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid tree entry size: %w", err)
			}
			entry.Size = &size
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

func buildCommitPayload(treeSHA string, parentSHAs []string, author, committer GitSignature, message, signature string) (string, error) {
	authorLine, err := formatCommitIdentityLine(author)
	if err != nil {
		return "", err
	}
	committerLine, err := formatCommitIdentityLine(committer)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("tree ")
	b.WriteString(treeSHA)
	b.WriteByte('\n')
	for _, parentSHA := range parentSHAs {
		b.WriteString("parent ")
		b.WriteString(parentSHA)
		b.WriteByte('\n')
	}
	b.WriteString("author ")
	b.WriteString(authorLine)
	b.WriteByte('\n')
	b.WriteString("committer ")
	b.WriteString(committerLine)
	b.WriteByte('\n')
	if strings.TrimSpace(signature) != "" {
		for i, line := range strings.Split(strings.TrimRight(signature, "\n"), "\n") {
			if i == 0 {
				b.WriteString("gpgsig ")
			} else {
				b.WriteByte(' ')
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	b.WriteByte('\n')
	b.WriteString(message)
	return b.String(), nil
}

func buildTagPayload(tagName, objectSHA, objectType string, tagger GitSignature, message string) (string, error) {
	taggerLine, err := formatCommitIdentityLine(tagger)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("object ")
	b.WriteString(objectSHA)
	b.WriteByte('\n')
	b.WriteString("type ")
	b.WriteString(objectType)
	b.WriteByte('\n')
	b.WriteString("tag ")
	b.WriteString(tagName)
	b.WriteByte('\n')
	b.WriteString("tagger ")
	b.WriteString(taggerLine)
	b.WriteByte('\n')
	b.WriteByte('\n')
	b.WriteString(message)
	if !strings.HasSuffix(message, "\n") {
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func validGitTagObjectType(t string) bool {
	switch t {
	case "commit", "tree", "blob":
		return true
	default:
		return false
	}
}

func formatCommitIdentityLine(sig GitSignature) (string, error) {
	t, err := parseGitSignatureTime(sig.Date)
	if err != nil {
		return "", fmt.Errorf("invalid signature time %q: %w", sig.Date, err)
	}
	return fmt.Sprintf("%s <%s> %d %s", sig.Name, sig.Email, t.Unix(), t.Format("-0700")), nil
}

func parseGitSignatureTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, raw)
}

func parseRawCommitObject(sha, raw string) (GitCommitObject, error) {
	headersPart, message, found := strings.Cut(raw, "\n\n")
	if !found {
		return GitCommitObject{}, fmt.Errorf("invalid raw commit object")
	}

	var (
		headers      []rawCommitHeader
		current      *rawCommitHeader
		lines        = strings.SplitAfter(headersPart, "\n")
		payloadBuild strings.Builder
	)
	for _, line := range lines {
		if strings.HasPrefix(line, " ") {
			if current == nil {
				return GitCommitObject{}, fmt.Errorf("invalid continuation line in commit object")
			}
			current.raw += line
			current.valueLines = append(current.valueLines, strings.TrimSuffix(strings.TrimPrefix(line, " "), "\n"))
			continue
		}

		line = strings.TrimSuffix(line, "\n")
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			return GitCommitObject{}, fmt.Errorf("invalid commit header %q", line)
		}
		header := rawCommitHeader{
			key:        parts[0],
			raw:        line + "\n",
			valueLines: []string{parts[1]},
		}
		headers = append(headers, header)
		current = &headers[len(headers)-1]
	}

	commit := GitCommitObject{
		SHA:     sha,
		Message: strings.TrimRight(message, "\n"),
	}

	for _, header := range headers {
		value := strings.Join(header.valueLines, "\n")
		switch header.key {
		case "tree":
			commit.TreeSHA = value
		case "parent":
			commit.ParentSHAs = append(commit.ParentSHAs, value)
		case "author":
			sig, err := parseCommitIdentityHeader(value)
			if err != nil {
				return GitCommitObject{}, err
			}
			commit.Author = sig
		case "committer":
			sig, err := parseCommitIdentityHeader(value)
			if err != nil {
				return GitCommitObject{}, err
			}
			commit.Committer = sig
		case "gpgsig":
			commit.Signature = value
			if commit.Signature != "" {
				commit.Signature += "\n"
			}
			continue
		}
		payloadBuild.WriteString(header.raw)
	}
	payloadBuild.WriteByte('\n')
	payloadBuild.WriteString(message)
	commit.Payload = payloadBuild.String()

	return commit, nil
}

func parseRawTagObject(sha, raw string) (GitTagObject, error) {
	headersPart, message, found := strings.Cut(raw, "\n\n")
	if !found {
		return GitTagObject{}, fmt.Errorf("invalid raw tag object")
	}

	tag := GitTagObject{
		SHA:     sha,
		Message: strings.TrimRight(message, "\n"),
		Payload: raw,
	}
	for _, line := range strings.Split(headersPart, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			return GitTagObject{}, fmt.Errorf("invalid tag header %q", line)
		}
		switch key {
		case "object":
			tag.ObjectSHA = value
		case "type":
			tag.ObjectType = value
		case "tag":
			tag.Tag = value
		case "tagger":
			sig, err := parseCommitIdentityHeader(value)
			if err != nil {
				return GitTagObject{}, err
			}
			tag.Tagger = sig
		}
	}
	if tag.ObjectSHA == "" || tag.ObjectType == "" || tag.Tag == "" {
		return GitTagObject{}, fmt.Errorf("invalid raw tag object")
	}
	return tag, nil
}

type rawCommitHeader struct {
	key        string
	raw        string
	valueLines []string
}

func parseCommitIdentityHeader(raw string) (GitSignature, error) {
	endEmail := strings.LastIndex(raw, "> ")
	startEmail := strings.LastIndex(raw, " <")
	if startEmail == -1 || endEmail == -1 || endEmail <= startEmail {
		return GitSignature{}, fmt.Errorf("invalid commit identity %q", raw)
	}

	name := raw[:startEmail]
	email := raw[startEmail+2 : endEmail]
	rest := strings.Fields(raw[endEmail+2:])
	if len(rest) < 2 {
		return GitSignature{}, fmt.Errorf("invalid commit identity timestamp %q", raw)
	}

	secs, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil {
		return GitSignature{}, fmt.Errorf("invalid commit identity timestamp %q: %w", raw, err)
	}
	offset, err := parseGitTZOffset(rest[1])
	if err != nil {
		return GitSignature{}, err
	}
	loc := time.FixedZone("", offset)
	t := time.Unix(secs, 0).In(loc)

	return GitSignature{
		Name:  name,
		Email: email,
		Date:  t.Format(time.RFC3339),
	}, nil
}

func parseGitTZOffset(raw string) (int, error) {
	if len(raw) != 5 {
		return 0, fmt.Errorf("invalid timezone offset %q", raw)
	}
	sign := 1
	switch raw[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return 0, fmt.Errorf("invalid timezone offset %q", raw)
	}
	hours, err := strconv.Atoi(raw[1:3])
	if err != nil {
		return 0, fmt.Errorf("invalid timezone offset %q: %w", raw, err)
	}
	minutes, err := strconv.Atoi(raw[3:5])
	if err != nil {
		return 0, fmt.Errorf("invalid timezone offset %q: %w", raw, err)
	}
	return sign * ((hours * 60 * 60) + (minutes * 60)), nil
}
