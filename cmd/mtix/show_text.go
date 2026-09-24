// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"time"
	"unicode"

	"github.com/hyper-swe/mtix/internal/format"
)

// showValueIndent aligns continuation lines under the value column of
// `mtix show` ("Desc:     " and "Prompt:   " are both ten characters wide).
const showValueIndent = "          "

// annotationTextIndent indents continuation lines of an annotation's text
// under its header line.
const annotationTextIndent = "      "

// displayText normalizes stored text for `mtix show` per FR-6.3 (MTIX-100.1):
// every control character except LF and TAB is removed, so a stray CR cannot
// overwrite a line, an escape sequence cannot restyle the terminal, and CRLF
// becomes LF; then leading blank lines and trailing whitespace are trimmed.
// Whitespace here is unicode.IsSpace throughout, so text that is only
// whitespace normalizes to "". The stored value is untouched; --json prints
// it raw.
func displayText(s string) string {
	s = strings.TrimRightFunc(format.StripControlChars(s), unicode.IsSpace)
	for {
		line, rest, found := strings.Cut(s, "\n")
		if !found || strings.TrimSpace(line) != "" {
			return s
		}
		s = rest
	}
}

// singleLine renders a field that must stay on one line, such as an author
// or addressee, per FR-3.4 (MTIX-100.1): displayText, with any remaining
// newline or tab turned into a space so the field cannot start a line of its
// own, and outer whitespace trimmed.
func singleLine(s string) string {
	s = strings.NewReplacer("\n", " ", "\t", " ").Replace(displayText(s))
	return strings.TrimSpace(s)
}

// indentContinuation indents every line after the first, per FR-6.3
// (MTIX-100.1), so no content line of a multi-line value starts at column 0
// where it could pass for a label (or for the "Annotations: none" marker).
// Blank lines stay empty rather than carrying trailing indentation.
func indentContinuation(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

// truncateChars cuts s to limit characters, the last three being "...", per
// FR-6.3 (MTIX-100.1). It counts runes, not bytes, so a cut never splits a
// multi-byte character: a split one would leave a stray byte, such as 0x9B,
// which a terminal can read as a control. A limit of 3 or less leaves s as is.
func truncateChars(s string, limit int) string {
	r := []rune(s)
	if limit <= 3 || len(r) <= limit {
		return s
	}
	return string(r[:limit-3]) + "..."
}

// annotationText is displayText for an annotation body per FR-3.4
// (MTIX-100.1), with a body that normalizes to nothing stated as "(empty)"
// rather than left blank.
func annotationText(s string) string {
	if t := displayText(s); t != "" {
		return t
	}
	return "(empty)"
}

// annotationTime renders an annotation timestamp as ISO-8601 UTC per FR-3.4
// (MTIX-100.1), or "unknown time" when none was recorded (a zero time would
// otherwise print as 0001-01-01T00:00:00Z).
func annotationTime(t time.Time) string {
	if t.IsZero() {
		return "unknown time"
	}
	return t.UTC().Format(time.RFC3339)
}
