// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Reader classification is conservative across executable wrappers and definitions.
package docs

import (
	"regexp"
	"strings"
)

var wakeReaderAppearanceRE = regexp.MustCompile(`(?:^|[^A-Za-z_0-9./-])(?:["']?(?:[A-Za-z_0-9./-]*/)?mtix["']?|["']?\$\{?MTIX_BIN\}?["']?)[ \t]+`)
var wakeFunctionStartRE = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z_0-9]*[ \t]*\([ \t]*\)|function[ \t]+[A-Za-z_][A-Za-z_0-9]*(?:[ \t]*\([ \t]*\))?)[ \t]*(?:\{|$)`)

func wakeReaderSource(text string) bool {
	for _, match := range wakeReaderAppearanceRE.FindAllStringIndex(text, -1) {
		if wakeInboxAfterFlags(text[match[1]:]) {
			return true
		}
	}
	return false
}

func wakeReaderWord(text string) (string, string) {
	match := wakeCommandWordRE.FindStringSubmatchIndex(text)
	if match == nil {
		return "", ""
	}
	return strings.Trim(text[match[2]:match[3]], "\"'"), text[match[1]:]
}

func wakeInboxAfterFlags(text string) bool {
	for {
		word, tail := wakeReaderWord(text)
		if word == "inbox" {
			next, _ := wakeReaderWord(tail)
			return next != "lease" && next != "ack"
		}
		if !strings.HasPrefix(word, "-") {
			return false
		}
		text = tail
		if strings.Contains(word, "=") || word == "--json" {
			continue
		}
		next, rest := wakeReaderWord(text)
		if next != "inbox" && !strings.HasPrefix(next, "-") {
			text = rest
		}
	}
}

func wakeReaderDefinitions(lines []string) []string {
	var problems []string
	var definition strings.Builder
	commented := false
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if definition.Len() == 0 {
			commented = strings.HasPrefix(line, "#")
			head := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			if !wakeFunctionStartRE.MatchString(head) {
				continue
			}
			if !strings.Contains(head, "{") && (index+1 == len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[index+1]), "{")) {
				continue
			}
		}
		if commented {
			line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		}
		definition.WriteString(line + "\n")
		depth, opened := wakeFunctionDepth(definition.String())
		if !opened || depth > 0 {
			continue
		}
		if wakeReaderSource(definition.String()) {
			problems = append(problems, definition.String())
		}
		definition.Reset()
	}
	if definition.Len() > 0 && wakeReaderSource(definition.String()) {
		problems = append(problems, definition.String())
	}
	return problems
}

func wakeFunctionDepth(text string) (int, bool) {
	depth, quote := 0, byte(0)
	opened, escaped, comment := false, false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if comment {
			if c == '\n' {
				comment = false
			}
			continue
		}
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '#':
			comment = true
		case '{':
			depth++
			opened = true
		case '}':
			depth--
		}
	}
	return depth, opened
}
