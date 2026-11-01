// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SafeText renders message text that may embed caller-supplied ids, such as a
// REST error message, a gRPC status message or an MCP tool result, so it cannot
// carry control, escape, bidirectional or other invisible characters
// (MTIX-187.7.8). Printable text, including non-ASCII letters, is kept. Every
// other character is escaped instead of removed, so the reader still sees what
// was there: \n, \r and \t for line breaks, \xNN for a byte up to U+00FF (and
// for each invalid UTF-8 byte), \uNNNN and \UNNNNNNNN above that. A backslash is
// NOT escaped, so that text already rendered by format.EventID (which holds
// backslash escapes) keeps its exact, strconv.Unquote-able form (MTIX-187.4.5).
// The cost is that a caller who sends the four characters backslash x 1 b gets the
// same message as one who sends a real ESC; read the escapes as a hint, and use
// the JSON fields for the exact value.
//
// The unsafe classes are the ones SafeLine removes: StripControlChars' classes
// (C0, DEL, C1, escape, bidirectional overrides and isolates, zero-width and tag
// characters, line and paragraph separators), the other invisible format
// characters (category Cf), the combining grapheme joiner and the Hangul
// fillers. The directional marks U+200E, U+200F and U+061C stay. Consolidating
// the two class lists is MTIX-187.7.19. Escaping never changes data: JSON
// fields keep the exact id. The output holds no unsafe rune, so SafeText is
// idempotent for every input.
func SafeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case unsafeText(r):
			b.WriteString(escapeRune(r))
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

func unsafeText(r rune) bool {
	switch r {
	case '\u200e', '\u200f', '\u061c':
		return false
	case '\n', '\r', '\t', '\u034f', '\u115f', '\u1160', '\u3164', '\uffa0':
		return true
	}
	return isUnsafeControl(r) || unicode.Is(unicode.Cf, r)
}

func escapeRune(r rune) string {
	switch r {
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	}
	switch {
	case r <= 0xff:
		return fmt.Sprintf(`\x%02x`, r)
	case r <= 0xffff:
		return fmt.Sprintf(`\u%04x`, r)
	}
	return fmt.Sprintf(`\U%08x`, r)
}
