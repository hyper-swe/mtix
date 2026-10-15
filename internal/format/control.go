// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"strings"
	"unicode"
)

// isInvisibleFormat reports whether r is an invisible Unicode format
// character that changes how surrounding text displays or hides content
// (MTIX-100.7): bidirectional embedding, override, isolate and mark controls
// (U+061C, U+200E, U+200F, U+202A-U+202E, U+2066-U+2069), zero-width
// characters (U+200B-U+200D, U+2060, U+FEFF) and the tag characters
// (U+E0001, U+E0020-U+E007F). Other format characters, such as the soft
// hyphen and variation selectors, are kept.
func isInvisibleFormat(r rune) bool {
	switch {
	case r == '\u061c', r == '\u200e', r == '\u200f':
		return true
	case r >= '\u200b' && r <= '\u200d':
		return true
	case r >= '\u202a' && r <= '\u202e':
		return true
	case r == '\u2060', r >= '\u2066' && r <= '\u2069', r == '\ufeff':
		return true
	case r == 0xe0001, r >= 0xe0020 && r <= 0xe007f:
		return true
	}
	return false
}

// isLineSeparator reports whether r is the Unicode line separator (U+2028) or
// paragraph separator (U+2029), which some renderers treat as a line break.
func isLineSeparator(r rune) bool {
	return r == '\u2028' || r == '\u2029'
}

// isUnsafeControl reports whether r must not reach a terminal from stored
// text: every Unicode control (C0, DEL, C1) except tab and newline, per
// FR-17.5 and FR-17 audit T10, plus the invisible format characters and the
// line and paragraph separators (MTIX-100.7). It is the one definition shared
// by the briefing sanitizer and StripControlChars.
func isUnsafeControl(r rune) bool {
	if isInvisibleFormat(r) || isLineSeparator(r) {
		return true
	}
	return r != '\t' && r != '\n' && unicode.IsControl(r)
}

// StripControlChars removes every unsafe character from s (see
// isUnsafeControl), keeping tab and newline. It judges the same runes as the
// briefing sanitizer but removes them instead of replacing them, for views a
// person reads, such as `mtix show`, where a replacement glyph would only be
// noise (MTIX-100.1). An escape sequence therefore loses its escape byte and
// prints as inert text; a carriage return can no longer overwrite a line; a
// bidirectional override can no longer reorder text. U+2028 and U+2029 become
// a newline, so they split a line in a defined way instead of whichever way a
// renderer picks (MTIX-100.7).
func StripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if isLineSeparator(r) {
			return '\n'
		}
		if isUnsafeControl(r) {
			return -1
		}
		return r
	}, s)
}
