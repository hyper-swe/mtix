// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format_test

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// unsafeInOneLine reports whether r must never appear in SafeLine output.
func unsafeInOneLine(r rune) bool {
	switch {
	case unicode.IsControl(r), r == '\u2028', r == '\u2029':
		return true
	case unicode.Is(unicode.Cf, r) && r != '\u200e' && r != '\u200f' && r != '\u061c':
		return true
	case r == '\u034f', r == '\u115f', r == '\u3164':
		return true
	case r >= '\u200b' && r <= '\u200d', r >= '\u202a' && r <= '\u202e':
		return true
	case r == '\u2060', r >= '\u2066' && r <= '\u2069', r == '\ufeff':
		return true
	case r == 0xe0001, r >= 0xe0020 && r <= 0xe007f:
		return true
	}
	return false
}

// TestSafeLine_HostileFixtures_OneSafeLine proves, for every shared hostile
// fixture, that the result is valid UTF-8 on one line with no control,
// bidi-override, zero-width or tag character (MTIX-187.7.4).
func TestSafeLine_HostileFixtures_OneSafeLine(t *testing.T) {
	all := append(testutil.HostileText(), testutil.HostileIDs()...)
	require.NotEmpty(t, all)
	for _, f := range all {
		t.Run(f.Name, func(t *testing.T) {
			got := format.SafeLine(f.Value)
			assert.True(t, utf8.ValidString(got))
			assert.Equal(t, strings.TrimSpace(got), got, "outer whitespace trimmed")
			for _, r := range got {
				assert.Falsef(t, unsafeInOneLine(r), "unsafe rune %U survived", r)
			}
		})
	}
}

func TestSafeLine_Table_ExactResults(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"plain", "Fix the bug", "Fix the bug"},
		{"printable non-ASCII stays readable", "Caf\u00e9 \u65e5\u672c\u8a9e \U0001F642", "Caf\u00e9 \u65e5\u672c\u8a9e \U0001F642"},
		{"esc csi loses its escape byte", "a\x1b[31mred\x1b[0m", "a[31mred[0m"},
		{"osc loses escape and bell", "x\x1b]0;owned\x07y", "x]0;ownedy"},
		{"cr cannot overwrite", "good\rEVIL", "goodEVIL"},
		{"crlf becomes one space", "a\r\nb", "a b"},
		{"lf and tab become spaces", "a\nb\tc", "a b c"},
		{"line and paragraph separators become spaces", "a\u2028b\u2029c", "a b c"},
		{"bidi override removed", "Approve \u202egnp.exe\u202c now", "Approve gnp.exe now"},
		{"zero width removed", "dele\u200bte\ufeff", "delete"},
		{"tag characters removed", "x\U000E0041y", "xy"},
		{"c1 controls removed", "a\u0085b\u009bc", "abc"},
		{"nul and del removed", "a\x00b\x7fc", "abc"},
		{"directional marks kept", "a\u200eb\u200fc\u061cd", "a\u200eb\u200fc\u061cd"},
		{"soft hyphen removed", "co\u00adop", "coop"},
		{"invisible operators removed", "a\u2061b\u2062c\u2063d\u2064e", "abcde"},
		{"deprecated format controls removed", "a\u206ab\u206fc", "abc"},
		{"mongolian vowel separator removed", "a\u180eb", "ab"},
		{"interlinear annotation marks removed", "a\ufff9b\ufffa\ufffbc", "abc"},
		{"combining grapheme joiner removed", "a\u034fb", "ab"},
		{"musical format char removed", "a\U0001d173b", "ab"},
		{"hangul fillers become spaces", "a\u115fb\u3164c", "a b c"},
		{"hangul jungseong and halfwidth fillers become spaces", "a\u1160b\uffa0c", "a b c"},
		{"trailing ideographic space trimmed", "title\u3000", "title"},
		{"outer whitespace trimmed", "  \n title \t\n", "title"},
		{"invalid utf8 becomes valid", "caf\xc3 bad", "caf\ufffd bad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, format.SafeLine(tt.in))
		})
	}
}

func TestSafeLine_LongInput_StaysLongAndSafe(t *testing.T) {
	in := strings.Repeat("a\x1b", 40000)
	got := format.SafeLine(in)
	assert.Equal(t, strings.Repeat("a", 40000), got)
}
