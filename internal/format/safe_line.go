// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"strings"
	"unicode"
)

// SafeLine renders stored ticket text that must print on a single terminal
// line, such as a title or an assignee (MTIX-187.7.4, FR-3.4). It is the one
// definition used by every human-output writer.
//
// It removes every unsafe character (see StripControlChars: C0, DEL, C1,
// escape and OSC lead bytes, carriage return, bidirectional overrides and
// isolates, zero-width and tag characters) and every other invisible format
// character (Unicode category Cf: soft hyphen, invisible operators, the
// deprecated format controls, interlinear annotation marks and the like) plus
// the combining grapheme joiner. The Hangul fillers, which draw as blanks,
// become spaces. Each newline, tab, line separator and paragraph separator
// also becomes one space, so the value cannot start a line of its own, and
// outer whitespace is trimmed. Printable non-ASCII text and the directional
// marks (U+200E, U+200F, U+061C) are kept, so titles stay readable. Invalid
// UTF-8 becomes U+FFFD. The stored value is untouched and --json prints it
// exactly.
func SafeLine(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(SafeBlock(s), "\n", " "))
}

// SafeBlock renders stored multi-line text for a terminal, such as an inbox
// comment body or a briefing block (MTIX-187.7.13). It strips and converts
// exactly the characters SafeLine does (they share safeLineRune and
// StripControlChars), but keeps newlines: CRLF becomes LF, the line and
// paragraph separators become newlines, tabs become spaces, leading blank
// lines and trailing whitespace are trimmed. Callers that print it inside a
// listing should indent its continuation lines so no stored line can start
// at column 0 and pass for a label or another entry.
func SafeBlock(s string) string {
	s = StripControlChars(s)
	s = strings.Map(safeLineRune, s)
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	for {
		line, rest, found := strings.Cut(s, "\n")
		if !found || strings.TrimSpace(line) != "" {
			return s
		}
		s = rest
	}
}

// safeLineRune drops invisible format characters, turns blank-drawing fillers
// into spaces and keeps everything else.
func safeLineRune(r rune) rune {
	switch r {
	case '\u200e', '\u200f', '\u061c': // directional marks stay (right-to-left text)
		return r
	case '\u115f', '\u1160', '\u3164', '\uffa0': // Hangul fillers draw as blanks
		return ' '
	case '\u034f': // combining grapheme joiner is invisible
		return -1
	}
	if unicode.Is(unicode.Cf, r) {
		return -1
	}
	return r
}
