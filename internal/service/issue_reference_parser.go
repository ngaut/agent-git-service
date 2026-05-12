package service

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// IssueReferenceMatch is a parsed GitHub-style issue/PR reference.
type IssueReferenceMatch struct {
	RepositoryFullName string
	Number             int
	RawReference       string
}

type issueReferenceMatchWithPos struct {
	IssueReferenceMatch
	start int
}

var (
	crossRepoIssueReferenceRE = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#([0-9]+)\b`)
	localIssueReferenceRE     = regexp.MustCompile(`(^|[^A-Za-z0-9_./-])#([0-9]+)\b`)
	issueReferenceURLRE       = regexp.MustCompile(`https?://[^\s<>()]+`)
)

// ParseIssueReferences recognizes #123, owner/repo#123, and issue/PR URLs in
// Markdown-ish text. It masks common non-rendered or code contexts first so
// agents do not get noisy backlinks from examples.
func ParseIssueReferences(body, sourceRepositoryFullName string) []IssueReferenceMatch {
	if strings.TrimSpace(body) == "" || strings.TrimSpace(sourceRepositoryFullName) == "" {
		return nil
	}
	body = maskIssueReferenceIgnoredMarkdown(body)

	var matches []issueReferenceMatchWithPos
	for _, loc := range issueReferenceURLRE.FindAllStringIndex(body, -1) {
		raw := strings.TrimRight(body[loc[0]:loc[1]], ".,;:!?)]}")
		repoFullName, number, ok := parseIssueReferenceURL(raw)
		if !ok {
			continue
		}
		matches = append(matches, issueReferenceMatchWithPos{
			IssueReferenceMatch: IssueReferenceMatch{
				RepositoryFullName: repoFullName,
				Number:             number,
				RawReference:       raw,
			},
			start: loc[0],
		})
	}

	for _, match := range crossRepoIssueReferenceRE.FindAllStringSubmatchIndex(body, -1) {
		repoStart, repoEnd := match[4], match[5]
		numStart, numEnd := match[6], match[7]
		number, err := strconv.Atoi(body[numStart:numEnd])
		if err != nil || number <= 0 {
			continue
		}
		matches = append(matches, issueReferenceMatchWithPos{
			IssueReferenceMatch: IssueReferenceMatch{
				RepositoryFullName: body[repoStart:repoEnd],
				Number:             number,
				RawReference:       body[repoStart:numEnd],
			},
			start: repoStart,
		})
	}

	for _, match := range localIssueReferenceRE.FindAllStringSubmatchIndex(body, -1) {
		hashStart := match[4] - 1
		numStart, numEnd := match[4], match[5]
		number, err := strconv.Atoi(body[numStart:numEnd])
		if err != nil || number <= 0 {
			continue
		}
		matches = append(matches, issueReferenceMatchWithPos{
			IssueReferenceMatch: IssueReferenceMatch{
				RepositoryFullName: sourceRepositoryFullName,
				Number:             number,
				RawReference:       body[hashStart:numEnd],
			},
			start: hashStart,
		})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].start < matches[j].start
	})

	seen := make(map[string]struct{}, len(matches))
	out := make([]IssueReferenceMatch, 0, len(matches))
	for _, match := range matches {
		key := strings.ToLower(match.RepositoryFullName) + "#" + strconv.Itoa(match.Number)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, match.IssueReferenceMatch)
	}
	return out
}

func parseIssueReferenceURL(raw string) (string, int, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", 0, false
	}
	path := strings.Trim(parsed.EscapedPath(), "/")
	if path == "" {
		return "", 0, false
	}
	parts := strings.Split(path, "/")
	if len(parts) < 4 {
		return "", 0, false
	}
	kind := parts[len(parts)-2]
	if kind != "issues" && kind != "pull" {
		return "", 0, false
	}
	number, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || number <= 0 {
		return "", 0, false
	}
	owner, err := url.PathUnescape(parts[len(parts)-4])
	if err != nil || owner == "" {
		return "", 0, false
	}
	repo, err := url.PathUnescape(parts[len(parts)-3])
	if err != nil || repo == "" {
		return "", 0, false
	}
	return owner + "/" + repo, number, true
}

func maskIssueReferenceIgnoredMarkdown(body string) string {
	if body == "" {
		return body
	}
	masked := []byte(body)
	maskHTMLComments(masked, body)
	maskFencedCodeBlocks(masked, body)
	maskInlineCodeSpans(masked, body)
	return string(masked)
}

func maskRange(masked []byte, start, end int) {
	if start < 0 {
		start = 0
	}
	if end > len(masked) {
		end = len(masked)
	}
	for i := start; i < end; i++ {
		if masked[i] != '\n' && masked[i] != '\r' {
			masked[i] = ' '
		}
	}
}

func maskHTMLComments(masked []byte, body string) {
	for pos := 0; pos < len(body); {
		startRel := strings.Index(body[pos:], "<!--")
		if startRel < 0 {
			return
		}
		start := pos + startRel
		endRel := strings.Index(body[start+4:], "-->")
		if endRel < 0 {
			maskRange(masked, start, len(body))
			return
		}
		end := start + 4 + endRel + 3
		maskRange(masked, start, end)
		pos = end
	}
}

func maskFencedCodeBlocks(masked []byte, body string) {
	inFence := false
	var fenceChar byte
	fenceLen := 0
	fenceStart := 0
	for lineStart := 0; lineStart < len(body); {
		lineEnd := strings.IndexByte(body[lineStart:], '\n')
		nextLineStart := len(body)
		if lineEnd >= 0 {
			lineEnd += lineStart
			nextLineStart = lineEnd + 1
		} else {
			lineEnd = len(body)
		}
		line := body[lineStart:lineEnd]
		ch, n, ok := markdownFence(line)
		if !inFence {
			if ok {
				inFence = true
				fenceChar = ch
				fenceLen = n
				fenceStart = lineStart
			}
		} else if ok && ch == fenceChar && n >= fenceLen {
			maskRange(masked, fenceStart, nextLineStart)
			inFence = false
		}
		lineStart = nextLineStart
	}
	if inFence {
		maskRange(masked, fenceStart, len(body))
	}
}

func markdownFence(line string) (byte, int, bool) {
	spaces := 0
	i := 0
	for i < len(line) && line[i] == ' ' && spaces < 4 {
		spaces++
		i++
	}
	if spaces > 3 || i >= len(line) {
		return 0, 0, false
	}
	ch := line[i]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	j := i
	for j < len(line) && line[j] == ch {
		j++
	}
	if j-i < 3 {
		return 0, 0, false
	}
	return ch, j - i, true
}

func maskInlineCodeSpans(masked []byte, body string) {
	for i := 0; i < len(body); i++ {
		if masked[i] == ' ' || body[i] != '`' {
			continue
		}
		runEnd := i + 1
		for runEnd < len(body) && body[runEnd] == '`' {
			runEnd++
		}
		runLen := runEnd - i
		closeStart := findInlineCodeClose(masked, body, runEnd, runLen)
		if closeStart < 0 {
			i = runEnd - 1
			continue
		}
		maskRange(masked, i, closeStart+runLen)
		i = closeStart + runLen - 1
	}
}

func findInlineCodeClose(masked []byte, body string, start, runLen int) int {
	for i := start; i <= len(body)-runLen; i++ {
		if masked[i] == ' ' || body[i] != '`' {
			continue
		}
		j := i
		for j < len(body) && body[j] == '`' {
			j++
		}
		if j-i == runLen {
			return i
		}
		i = j - 1
	}
	return -1
}
