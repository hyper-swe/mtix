// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// isInvisibleFormat reports whether r is an invisible Unicode format
// character that changes how surrounding text displays or hides content
// (MTIX-100.7): bidirectional embedding, override and isolate controls
// (U+202A-U+202E, U+2066-U+2069), zero-width characters (U+200B-U+200D,
// U+2060, U+FEFF) and the tag characters (U+E0001, U+E0020-U+E007F), which
// hide text from a reader. The directional marks (U+061C, U+200E, U+200F)
// are kept because right-to-left text needs them and they are not overrides:
// they do not force a whole run to display reversed, and they cannot reorder
// Latin letters. Known limit: a lone U+200F or U+061C can still change how
// digits and the neutral characters between them are ordered, so a number
// pair such as 0 / 12 can display as 12 / 0 in a bidirectional terminal; use
// --json for the exact text. Other format characters, such as the soft
// hyphen and variation selectors, are kept too.
func isInvisibleFormat(r rune) bool {
	switch {
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

// isAgentUnsafe reports whether r must not reach an LLM agent in text that a
// person is not reading (MTIX-100.8, FR-17.5): everything isUnsafeControl
// rejects, plus these classes, each of which can carry a payload no reviewer
// sees:
//   - every Unicode format character (category Cf): soft hyphen, invisible
//     operators, deprecated format controls, interlinear annotation marks, the
//     Mongolian vowel separator and the directional marks U+200E, U+200F and
//     U+061C, which have no reader here;
//   - every variation selector (U+FE0F directly after a symbol is the one
//     exception, see mapAgentText): U+FE00-U+FE0F, U+E0100-U+E01EF and the
//     Mongolian free variation selectors U+180B-U+180D and U+180F (a
//     byte-smuggling channel after an emoji);
//   - the combining grapheme joiner U+034F, the Khmer inherent vowels
//     U+17B4-U+17B5 and the Hangul fillers U+115F, U+1160, U+3164 and U+FFA0,
//     which draw as nothing or as blanks;
//   - every code point the Go Unicode tables leave unassigned (so the gaps of
//     the tag block, U+FFF0-U+FFF8 and U+2065 are covered); the
//     noncharacters U+FDD0-U+FDEF and U+xxFFFE, U+xxFFFF are unassigned too,
//     so they need no rule of their own.
//
// U+2800 braille blank is deliberately kept: it is an assigned symbol with
// visible width, not a hidden character.
//
// A code point Unicode assigns after the Go release mtix is built with is
// treated as unassigned until Go is upgraded. Private-use code points are
// kept. The human display rule (StripControlChars, SafeLine, SafeBlock) is
// separate and keeps the directional marks and selectors for readability. The
// briefing sanitizer and the inbox envelope share this one definition.
func isAgentUnsafe(r rune) bool {
	if isUnsafeControl(r) {
		return true
	}
	if r < utf8.RuneSelf {
		return false
	}
	switch {
	case unicode.Is(unicode.Cf, r), isUnassigned(r):
		return true
	case r >= 0xfe00 && r <= 0xfe0f, r >= 0xe0100 && r <= 0xe01ef:
		return true
	case r >= 0x180b && r <= 0x180d, r == 0x180f, r == 0x17b4, r == 0x17b5:
		return true
	case r == 0x034f, r == 0x115f, r == 0x1160, r == 0x3164, r == 0xffa0:
		return true
	}
	return false
}

// mapAgentText applies the agent-text rule to s: every rune isAgentUnsafe
// rejects is replaced by replacement (a negative value drops it), with one
// context-dependent exception. A single U+FE0F directly after a symbol rune
// (category So, which covers emoji bases) is kept, so ordinary emoji keep
// their presentation. U+FE0F anywhere else, a second selector after it and
// every other variation selector stay unsafe. Invalid UTF-8 decodes to
// U+FFFD, which never counts as a base.
func mapAgentText(s string, replacement rune) string {
	var b strings.Builder
	b.Grow(len(s))
	var prev rune
	for _, r := range s {
		switch {
		case r == 0xfe0f && isEmojiBase(prev):
			b.WriteRune(r)
		case isAgentUnsafe(r):
			if replacement >= 0 {
				b.WriteRune(replacement)
			}
		default:
			b.WriteRune(r)
		}
		prev = r
	}
	return b.String()
}

// isEmojiBase reports whether r may be followed by one kept U+FE0F: a symbol
// character (category So), not the replacement character that invalid UTF-8
// decodes to and not the braille blank U+2800, which is an So but not an emoji.
func isEmojiBase(r rune) bool {
	return r != utf8.RuneError && r != 0x2800 && unicode.Is(unicode.So, r)
}

// isUnassigned reports whether the Go Unicode tables place r in no general
// category (Cn). Surrogates and private use are categories, so they are not
// unassigned here.
func isUnassigned(r rune) bool {
	return !unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z,
		unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs)
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
