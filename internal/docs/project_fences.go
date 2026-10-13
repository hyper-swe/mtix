// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import "strings"

// projectPointerInText excludes fenced examples without changing any user bytes.
func projectPointerInText(content []byte, pointer, name string) (bool, bool) {
	var delimiter byte
	var length int
	lines := strings.FieldsFunc(string(content), func(r rune) bool { return r == '\n' || r == '\r' })
	for _, line := range lines {
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
		text := strings.TrimSpace(line)
		if text == pointer || (name == "AGENTS.md" && isAgentsPointer(text)) {
			return true, false
		}
	}
	return false, delimiter != 0
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
