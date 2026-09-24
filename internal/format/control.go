// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"strings"
	"unicode"
)

// isUnsafeControl reports whether r is a control character that must not
// reach a terminal from stored text: every Unicode control (C0, DEL, C1)
// except tab and newline, per FR-17.5 and FR-17 audit T10. It is the one
// definition shared by the briefing sanitizer and StripControlChars.
func isUnsafeControl(r rune) bool {
	return r != '\t' && r != '\n' && unicode.IsControl(r)
}

// StripControlChars removes every unsafe control character from s (see
// isUnsafeControl), keeping tab and newline. It judges the same runes as the
// briefing sanitizer but removes them instead of replacing them, for views a
// person reads, such as `mtix show`, where a replacement glyph would only be
// noise (MTIX-100.1). An escape sequence therefore loses its escape byte and
// prints as inert text; a carriage return can no longer overwrite a line.
func StripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if isUnsafeControl(r) {
			return -1
		}
		return r
	}, s)
}
