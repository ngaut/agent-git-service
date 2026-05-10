package service

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	mdast "github.com/yuin/goldmark/ast"
	mdtext "github.com/yuin/goldmark/text"
)

type wikiTextEdit struct {
	start       int
	stop        int
	replacement string
}

func rewriteWikiReferences(body, oldSlug, newSlug string) (string, bool, error) {
	if body == "" || oldSlug == newSlug {
		return body, false, nil
	}
	source := []byte(body)
	if !utf8.Valid(source) {
		return "", false, fmt.Errorf("page body is not valid UTF-8")
	}

	blocked, err := collectWikiProtectedOffsets(source)
	if err != nil {
		return "", false, err
	}

	edits := make([]wikiTextEdit, 0, 4)
	for i := 0; i < len(source); {
		if blocked[i] {
			i++
			continue
		}
		if i+1 < len(source) && source[i] == '[' && source[i+1] == '[' {
			next, edit, ok := scanWikiShorthand(source, blocked, i, oldSlug, newSlug)
			if ok {
				edits = append(edits, edit)
			}
			if next > i {
				i = next
				continue
			}
		}
		if source[i] == '[' && (i == 0 || source[i-1] != '!') {
			next, edit, ok := scanInlineMarkdownLink(source, blocked, i, oldSlug, newSlug)
			if ok {
				edits = append(edits, edit)
			}
			if next > i {
				i = next
				continue
			}
		}
		if i == 0 || source[i-1] == '\n' {
			next, edit, ok := scanReferenceDefinition(source, blocked, i, oldSlug, newSlug)
			if ok {
				edits = append(edits, edit)
			}
			if next > i {
				i = next
				continue
			}
		}
		i++
	}

	if len(edits) == 0 {
		return body, false, nil
	}
	return applyWikiTextEdits(body, edits), true, nil
}

func collectWikiProtectedOffsets(source []byte) (_ []bool, err error) {
	blocked := make([]bool, len(source))
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("markdown rewrite parse failed: %v", r)
		}
	}()

	parser := goldmark.New().Parser()
	root := parser.Parse(mdtext.NewReader(source))
	if walkErr := mdast.Walk(root, func(n mdast.Node, entering bool) (mdast.WalkStatus, error) {
		if !entering {
			return mdast.WalkContinue, nil
		}
		switch node := n.(type) {
		case *mdast.FencedCodeBlock, *mdast.CodeBlock:
			markNodeLines(blocked, n.Lines())
		case *mdast.CodeSpan:
			for child := node.FirstChild(); child != nil; child = child.NextSibling() {
				textNode, ok := child.(*mdast.Text)
				if !ok {
					continue
				}
				markWikiRange(blocked, textNode.Segment.Start, textNode.Segment.Stop)
			}
		case *mdast.HTMLBlock:
			if hasProtectedHTMLTag(string(node.Text(source))) {
				markNodeLines(blocked, node.Lines())
				if node.HasClosure() {
					markWikiRange(blocked, node.ClosureLine.Start, node.ClosureLine.Stop)
				}
			}
		}
		return mdast.WalkContinue, nil
	}); walkErr != nil {
		return nil, walkErr
	}

	markProtectedHTMLSections(source, blocked)
	return blocked, nil
}

func hasProtectedHTMLTag(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, "<code") ||
		strings.Contains(lower, "<pre") ||
		strings.Contains(lower, "<script") ||
		strings.Contains(lower, "<style")
}

func markProtectedHTMLSections(source []byte, blocked []bool) {
	lower := strings.ToLower(string(source))
	for _, tag := range []string{"code", "pre", "script", "style"} {
		openToken := "<" + tag
		closeToken := "</" + tag + ">"
		searchFrom := 0
		for searchFrom < len(lower) {
			offset := strings.Index(lower[searchFrom:], openToken)
			if offset < 0 {
				break
			}
			start := searchFrom + offset
			if blocked[start] {
				searchFrom = start + len(openToken)
				continue
			}
			gt := strings.IndexByte(lower[start:], '>')
			if gt < 0 {
				markWikiRange(blocked, start, len(source))
				break
			}
			contentStart := start + gt + 1
			closeOffset := strings.Index(lower[contentStart:], closeToken)
			if closeOffset < 0 {
				markWikiRange(blocked, start, len(source))
				break
			}
			end := contentStart + closeOffset + len(closeToken)
			markWikiRange(blocked, start, end)
			searchFrom = end
		}
	}
}

func markNodeLines(blocked []bool, lines *mdtext.Segments) {
	if lines == nil {
		return
	}
	for i := 0; i < lines.Len(); i++ {
		segment := lines.At(i)
		markWikiRange(blocked, segment.Start, segment.Stop)
	}
}

func markWikiRange(blocked []bool, start, stop int) {
	if start < 0 {
		start = 0
	}
	if stop > len(blocked) {
		stop = len(blocked)
	}
	for i := start; i < stop; i++ {
		blocked[i] = true
	}
}

func scanWikiShorthand(source []byte, blocked []bool, start int, oldSlug, newSlug string) (int, wikiTextEdit, bool) {
	for i := start + 2; i+1 < len(source); i++ {
		if blocked[i] {
			return start, wikiTextEdit{}, false
		}
		if source[i] == '\n' {
			return start, wikiTextEdit{}, false
		}
		if source[i] == ']' && source[i+1] == ']' {
			replacement, changed := rewriteWikiTargetLiteral(string(source[start+2:i]), oldSlug, newSlug)
			if !changed {
				return i + 2, wikiTextEdit{}, false
			}
			return i + 2, wikiTextEdit{start: start + 2, stop: i, replacement: replacement}, true
		}
	}
	return start, wikiTextEdit{}, false
}

func scanInlineMarkdownLink(source []byte, blocked []bool, start int, oldSlug, newSlug string) (int, wikiTextEdit, bool) {
	labelEnd := findMarkdownLabelEnd(source, blocked, start)
	if labelEnd < 0 || labelEnd+1 >= len(source) || source[labelEnd+1] != '(' {
		return start, wikiTextEdit{}, false
	}
	destStart, destStop, end, ok := parseInlineDestinationSpan(source, blocked, labelEnd+2)
	if !ok {
		return start, wikiTextEdit{}, false
	}
	replacement, changed := rewriteWikiTargetLiteral(string(source[destStart:destStop]), oldSlug, newSlug)
	if !changed {
		return end, wikiTextEdit{}, false
	}
	return end, wikiTextEdit{start: destStart, stop: destStop, replacement: replacement}, true
}

func scanReferenceDefinition(source []byte, blocked []bool, start int, oldSlug, newSlug string) (int, wikiTextEdit, bool) {
	i := start
	for i < len(source) && i < start+3 && source[i] == ' ' {
		i++
	}
	if i >= len(source) || source[i] != '[' {
		return start, wikiTextEdit{}, false
	}
	labelEnd := findMarkdownLabelEnd(source, blocked, i)
	if labelEnd < 0 || labelEnd+1 >= len(source) || source[labelEnd+1] != ':' {
		return start, wikiTextEdit{}, false
	}
	lineEnd := i
	for lineEnd < len(source) && source[lineEnd] != '\n' {
		lineEnd++
	}
	destStart, destStop, ok := parseReferenceDestinationSpan(source, blocked, labelEnd+2, lineEnd)
	if !ok {
		return lineEnd, wikiTextEdit{}, false
	}
	replacement, changed := rewriteWikiTargetLiteral(string(source[destStart:destStop]), oldSlug, newSlug)
	if !changed {
		return lineEnd, wikiTextEdit{}, false
	}
	return lineEnd, wikiTextEdit{start: destStart, stop: destStop, replacement: replacement}, true
}

func findMarkdownLabelEnd(source []byte, blocked []bool, start int) int {
	depth := 0
	for i := start + 1; i < len(source); i++ {
		if blocked[i] {
			return -1
		}
		if source[i] == '\n' {
			return -1
		}
		if source[i] == '\\' {
			i++
			continue
		}
		switch source[i] {
		case '[':
			depth++
		case ']':
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

func parseInlineDestinationSpan(source []byte, blocked []bool, start int) (int, int, int, bool) {
	i := start
	for i < len(source) && (source[i] == ' ' || source[i] == '\t') {
		i++
	}
	if i >= len(source) || blocked[i] || source[i] == '\n' {
		return 0, 0, 0, false
	}
	if source[i] == '<' {
		destStart := i + 1
		for i = destStart; i < len(source); i++ {
			if blocked[i] || source[i] == '\n' {
				return 0, 0, 0, false
			}
			if source[i] == '>' {
				destStop := i
				i++
				for i < len(source) && (source[i] == ' ' || source[i] == '\t') {
					i++
				}
				for i < len(source) && source[i] != ')' {
					if blocked[i] || source[i] == '\n' {
						return 0, 0, 0, false
					}
					if source[i] == '\\' {
						i++
					}
					i++
				}
				if i >= len(source) || source[i] != ')' {
					return 0, 0, 0, false
				}
				return destStart, destStop, i + 1, true
			}
		}
		return 0, 0, 0, false
	}

	destStart := i
	depth := 0
	destStop := -1
	for i < len(source) {
		if blocked[i] || source[i] == '\n' {
			return 0, 0, 0, false
		}
		if source[i] == '\\' {
			i += 2
			continue
		}
		switch source[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				if destStop < 0 {
					destStop = i
				}
				return destStart, destStop, i + 1, true
			}
			depth--
		case ' ', '\t':
			if destStop < 0 {
				destStop = i
			}
		}
		i++
	}
	return 0, 0, 0, false
}

func parseReferenceDestinationSpan(source []byte, blocked []bool, start, lineEnd int) (int, int, bool) {
	i := start
	for i < lineEnd && (source[i] == ' ' || source[i] == '\t') {
		i++
	}
	if i >= lineEnd || blocked[i] {
		return 0, 0, false
	}
	if source[i] == '<' {
		destStart := i + 1
		for i = destStart; i < lineEnd; i++ {
			if blocked[i] {
				return 0, 0, false
			}
			if source[i] == '>' {
				return destStart, i, true
			}
		}
		return 0, 0, false
	}

	destStart := i
	for i < lineEnd {
		if blocked[i] {
			return 0, 0, false
		}
		if source[i] == ' ' || source[i] == '\t' {
			break
		}
		i++
	}
	if i == destStart {
		return 0, 0, false
	}
	return destStart, i, true
}

func rewriteWikiTargetLiteral(raw, oldSlug, newSlug string) (string, bool) {
	trimStart, trimStop := trimWhitespaceBounds(raw)
	if trimStart == trimStop {
		return "", false
	}
	target := raw[trimStart:trimStop]
	if normalizeWikiReference(target) != oldSlug {
		return "", false
	}

	pathPart := target
	suffix := ""
	if idx := strings.IndexAny(target, "?#"); idx >= 0 {
		pathPart = target[:idx]
		suffix = target[idx:]
	}

	prefix := ""
	switch {
	case strings.HasPrefix(pathPart, "./"):
		prefix = "./"
		pathPart = strings.TrimPrefix(pathPart, "./")
	case strings.HasPrefix(pathPart, "/"):
		prefix = "/"
		pathPart = strings.TrimPrefix(pathPart, "/")
	}

	hadExt := strings.HasSuffix(pathPart, wikiPageExt)
	replacement := prefix + newSlug
	if hadExt {
		replacement += wikiPageExt
	}
	replacement += suffix
	return raw[:trimStart] + replacement + raw[trimStop:], true
}

func trimWhitespaceBounds(raw string) (int, int) {
	start := 0
	for start < len(raw) && (raw[start] == ' ' || raw[start] == '\t') {
		start++
	}
	stop := len(raw)
	for stop > start && (raw[stop-1] == ' ' || raw[stop-1] == '\t') {
		stop--
	}
	return start, stop
}

func applyWikiTextEdits(body string, edits []wikiTextEdit) string {
	var out strings.Builder
	out.Grow(len(body))
	cursor := 0
	for _, edit := range edits {
		if edit.start < cursor {
			continue
		}
		out.WriteString(body[cursor:edit.start])
		out.WriteString(edit.replacement)
		cursor = edit.stop
	}
	out.WriteString(body[cursor:])
	return out.String()
}
