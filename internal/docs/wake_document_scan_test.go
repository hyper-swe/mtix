// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Document scanning checks marked commands; ordinary prose is not executable guidance.
package docs

import (
	"regexp"
	"strings"
)

var wakeReaderReferenceRE = regexp.MustCompile(`^(?:command (?:-- )?)?mtix(?: (?:[A-Za-z_0-9./:=-]+(?:\\\|[A-Za-z_0-9./:-]+)*|<[A-Za-z_-]+>|"\$\{?[A-Za-z_][A-Za-z_0-9]*\}?"))*$`)
var wakeConsumerRE = regexp.MustCompile(`\b(?:claude|codex|agent|xargs|eval)\b|--message\b`)
var wakeReferenceRE = regexp.MustCompile(`^\$\{?[A-Za-z_][A-Za-z_0-9]*\}?(?:/[A-Za-z_0-9./-]*)?$`)
var wakeFenceRE = regexp.MustCompile("^\\s*(?:>\\s*)?(`{3,}|~{3,})(.*)$")

// wakeDocumentCommands follows the contributor markup convention: code spans and
// executable fences contain commands; plain prose and lone variable spans are
// references. Shell examples retain complete lines, including their comments.
func wakeDocumentCommands(text string) []string {
	var commands []string
	var prose strings.Builder
	flush := func() { commands = append(commands, wakeCodeSpans(prose.String())...); prose.Reset() }
	fence, executable := "", false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\\\n", ""), "\n") {
		if match := wakeFenceRE.FindStringSubmatch(line); match != nil {
			flush()
			if fence == "" {
				fence = match[1]
				info := strings.Fields(strings.ToLower(match[2]))
				executable = len(info) == 0 || info[0] == "sh" || info[0] == "bash" || info[0] == "shell" || info[0] == "console"
			} else if match[1][0] == fence[0] && len(match[1]) >= len(fence) && strings.TrimSpace(match[2]) == "" {
				fence = ""
			}
			continue
		}
		if fence != "" {
			if executable {
				commands = append(commands, line)
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
		} else {
			prose.WriteString(line + "\n")
		}
	}
	flush()
	return commands
}

func wakeCodeSpans(line string) []string {
	var spans []string
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			return spans
		}
		run := 1
		for start+run < len(line) && line[start+run] == '`' {
			run++
		}
		line = line[start+run:]
		end := wakeCodeSpanEnd(line, run)
		if end < 0 {
			continue
		}
		span := strings.TrimSpace(strings.ReplaceAll(line[:end], "\n", " "))
		if !wakeReferenceRE.MatchString(span) {
			spans = append(spans, span)
		}
		line = line[end+run:]
	}
}

func wakeCodeSpanEnd(text string, length int) int {
	for offset := 0; offset < len(text); {
		next := strings.IndexByte(text[offset:], '`')
		if next < 0 {
			return -1
		}
		start := offset + next
		end := start + 1
		for end < len(text) && text[end] == '`' {
			end++
		}
		if end-start == length {
			return start
		}
		offset = end
	}
	return -1
}
