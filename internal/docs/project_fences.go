// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import "strings"

type projectPointerSpan struct{ start, end int }

// projectPointerSpans shares live instruction recognition with diagnosis and
// normalization. Spans include only their own original line terminators.
func projectPointerSpans(content []byte, pointer, name string) ([]projectPointerSpan, bool) {
	var spans []projectPointerSpan
	var delimiter byte
	var length int
	text := string(content)
	for offset := 0; offset < len(text); {
		start := offset
		end, next := projectLineEnd(text, offset)
		line := text[offset:end]
		offset = next
		char, run, suffix := projectFenceLine(line)
		if delimiter != 0 {
			if char == delimiter && run >= length && strings.Trim(suffix, " \t") == "" {
				delimiter, length = 0, 0
			}
			continue
		}
		if run >= 3 && (char != '`' || !strings.Contains(suffix, "`")) {
			delimiter, length = char, run
			continue
		}
		if projectPointerIndentEligible(line) && isProjectPointer(line, pointer, name) {
			spans = append(spans, projectPointerSpan{start, next})
		}
	}
	return spans, delimiter != 0
}

func projectLineEnd(text string, start int) (int, int) {
	end := strings.IndexAny(text[start:], "\r\n")
	if end < 0 {
		return len(text), len(text)
	}
	end += start
	next := end + 1
	if text[end] == '\r' && next < len(text) && text[next] == '\n' {
		next++
	}
	return end, next
}

func isProjectPointer(line, pointer, name string) bool {
	text := strings.TrimSpace(line)
	return text == pointer || (name == "AGENTS.md" && isAgentsPointer(text))
}

func removeProjectPointerDuplicates(content []byte, spans []projectPointerSpan) []byte {
	if len(spans) < 2 {
		return content
	}
	out := make([]byte, 0, len(content))
	offset := 0
	for _, span := range spans[1:] {
		out = append(out, content[offset:span.start]...)
		offset = span.end
	}
	return append(out, content[offset:]...)
}

// projectFenceLine applies CommonMark's three-space indentation limit and counts
// one delimiter kind; suffix validity depends on whether this opens or closes.
func projectFenceLine(line string) (byte, int, string) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent == len(line) {
		return 0, 0, ""
	}
	char := line[indent]
	if char != '`' && char != '~' {
		return 0, 0, ""
	}
	end := indent
	for end < len(line) && line[end] == char {
		end++
	}
	return char, end - indent, line[end:]
}

// projectPointerIndentEligible recognizes standalone instructions rather than
// deeper examples or paragraph continuations; it is not a Markdown block parser.
func projectPointerIndentEligible(line string) bool {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	return indent <= 3 && (indent == len(line) || line[indent] != '\t')
}
