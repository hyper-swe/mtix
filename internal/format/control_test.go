// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStripControlChars_UnsafeRunes_RemovedTabAndNewlineKept verifies the
// removal variant of the FR-17.5 control-character rule (MTIX-100.1).
func TestStripControlChars_UnsafeRunes_RemovedTabAndNewlineKept(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text unchanged", "PASS: all good", "PASS: all good"},
		{"tab and newline kept", "a\tb\nc", "a\tb\nc"},
		{"escape removed", "x\x1b[31my", "x[31my"},
		{"carriage return removed", "FAIL\r PASS", "FAIL PASS"},
		{"NUL, BEL and DEL removed", "a\x00b\x07c\x7fd", "abcd"},
		{"C1 controls removed", "a\u0085b\u009bc", "abc"},
		{"non-ASCII letters kept", "é中ü", "é中ü"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, StripControlChars(tt.in))
		})
	}
}

// TestSanitizeControlChars_SameRunesAsStrip_ReplacesInsteadOfRemoving pins
// that both functions judge the same runes unsafe; only the treatment
// differs (replacement for briefings, removal for show).
func TestSanitizeControlChars_SameRunesAsStrip_ReplacesInsteadOfRemoving(t *testing.T) {
	in := "a\tb\nc\x1bd\re\x7ff\u009bg"
	assert.Equal(t, "a\tb\nc�d�e�f�g", sanitizeControlChars(in))
	assert.Equal(t, "a\tb\ncdefg", StripControlChars(in))
}

// invisibleClasses lists one rune per class of invisible Unicode format
// character that StripControlChars and the briefing sanitizer handle
// (MTIX-100.7).
var invisibleClasses = []struct {
	name string
	r    rune
}{
	{"left-to-right embedding U+202A", '\u202a'},
	{"right-to-left embedding U+202B", '\u202b'},
	{"pop directional formatting U+202C", '\u202c'},
	{"left-to-right override U+202D", '\u202d'},
	{"right-to-left override U+202E", '\u202e'},
	{"left-to-right isolate U+2066", '\u2066'},
	{"right-to-left isolate U+2067", '\u2067'},
	{"first strong isolate U+2068", '\u2068'},
	{"pop directional isolate U+2069", '\u2069'},
	{"zero width space U+200B", '\u200b'},
	{"zero width non-joiner U+200C", '\u200c'},
	{"zero width joiner U+200D", '\u200d'},
	{"word joiner U+2060", '\u2060'},
	{"byte order mark U+FEFF", '\ufeff'},
	{"tag space U+E0020", '\U000e0020'},
	{"tag letter U+E0041", '\U000e0041'},
	{"cancel tag U+E007F", '\U000e007f'},
}

// TestStripControlChars_InvisibleFormatChars_RemovedPerClass verifies that
// bidirectional and zero-width characters are removed, one class at a time.
func TestStripControlChars_InvisibleFormatChars_RemovedPerClass(t *testing.T) {
	for _, tt := range invisibleClasses {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, "ab", StripControlChars("a"+string(tt.r)+"b"))
			assert.Equal(t, "", StripControlChars(string(tt.r)))
		})
	}
}

// TestStripControlChars_BidiOverrideVerdict_ReadsAsStored verifies the
// motivating case: the reversed text no longer reorders on screen.
func TestStripControlChars_BidiOverrideVerdict_ReadsAsStored(t *testing.T) {
	assert.Equal(t, "FAIL SSAP ok", StripControlChars("FAIL \u202eSSAP\u202c ok"))
}

// TestStripControlChars_LineAndParagraphSeparator_BecomeNewline pins the
// defined behaviour for U+2028 and U+2029: each becomes one newline.
func TestStripControlChars_LineAndParagraphSeparator_BecomeNewline(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"line separator", "a\u2028b", "a\nb"},
		{"paragraph separator", "a\u2029b", "a\nb"},
		{"both", "a\u2028\u2029b", "a\n\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, StripControlChars(tt.in))
		})
	}
}

// TestStripControlChars_OrdinaryFormatAndSpaceChars_Kept pins what stays:
// variation selectors, the no-break space and non-Latin text.
func TestStripControlChars_OrdinaryFormatAndSpaceChars_Kept(t *testing.T) {
	in := "a b️c　d العربية"
	assert.Equal(t, in, StripControlChars(in))
}

// TestStripControlChars_DirectionalMarks_Kept pins that the left-to-right
// mark, right-to-left mark and Arabic letter mark stay: they cannot reorder
// or hide text, and right-to-left text needs them (MTIX-100.7).
func TestStripControlChars_DirectionalMarks_Kept(t *testing.T) {
	for _, r := range []rune{'\u200e', '\u200f', '\u061c'} {
		in := "a" + string(r) + "b"
		assert.Equal(t, in, StripControlChars(in))
		assert.Equal(t, in, sanitizeControlChars(in))
	}
}

// TestSanitizeControlChars_InvisibleFormatChars_ReplacedPerClass pins the
// briefing decision: it takes the same classes as show and replaces each
// rune (U+2028 and U+2029 included) with U+FFFD, so the change stays visible.
func TestSanitizeControlChars_InvisibleFormatChars_ReplacedPerClass(t *testing.T) {
	classes := append([]struct {
		name string
		r    rune
	}{
		{"line separator U+2028", '\u2028'},
		{"paragraph separator U+2029", '\u2029'},
	}, invisibleClasses...)
	for _, tt := range classes {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, "a�b", sanitizeControlChars("a"+string(tt.r)+"b"))
		})
	}
}
