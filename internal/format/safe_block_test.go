// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// TestSafeBlock_HostileFixtures_NoUnsafeRunesButNewlines proves, for every
// shared hostile fixture, that SafeBlock output is valid UTF-8 and holds only
// newlines among the controls (MTIX-187.7.13).
func TestSafeBlock_HostileFixtures_NoUnsafeRunesButNewlines(t *testing.T) {
	all := append(testutil.HostileText(), testutil.HostileIDs()...)
	require.NotEmpty(t, all)
	for _, f := range all {
		t.Run(f.Name, func(t *testing.T) {
			got := format.SafeBlock(f.Value)
			assert.True(t, utf8.ValidString(got))
			assert.NotContains(t, got, "\t")
			assert.NotContains(t, got, "\r")
			for _, r := range strings.ReplaceAll(got, "\n", "") {
				assert.Falsef(t, unsafeInOneLine(r), "unsafe rune %U survived", r)
			}
			assert.Equal(t, strings.TrimRight(got, " \n"), got, "no trailing whitespace")
			assert.False(t, strings.HasPrefix(got, "\n"), "no leading blank line")
		})
	}
}

func TestSafeBlock_Table_ExactResults(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"newlines kept", "a\nb\nc", "a\nb\nc"},
		{"crlf becomes lf", "a\r\nb", "a\nb"},
		{"lone cr removed", "good\rEVIL", "goodEVIL"},
		{"line and paragraph separators become newlines", "a\u2028b\u2029c", "a\nb\nc"},
		{"tab becomes space", "a\tb", "a b"},
		{"esc and osc lose their lead byte", "a\x1b[31mb\x1b]0;t\x07c", "a[31mb]0;tc"},
		{"bidi override removed", "x\u202ey", "xy"},
		{"zero width removed", "a\u200bb\ufeffc", "abc"},
		{"soft hyphen and invisible operators removed", "co\u00adop\u2061", "coop"},
		{"hangul filler becomes space", "a\u3164b", "a b"},
		{"hangul jungseong filler becomes space", "a\u1160b", "a b"},
		{"halfwidth hangul filler becomes space", "a\uffa0b", "a b"},
		{"hangul choseong filler becomes space", "a\u115fb", "a b"},
		{"trailing ideographic space trimmed", "body\u3000\u00a0", "body"},
		{"combining grapheme joiner removed", "a\u034fb", "ab"},
		{"directional marks kept", "a\u200eb\u200fc\u061cd", "a\u200eb\u200fc\u061cd"},
		{"printable non-ASCII kept", "Caf\u00e9 \u65e5\u672c\u8a9e", "Caf\u00e9 \u65e5\u672c\u8a9e"},
		{"leading blank lines trimmed", "\n \n\nbody", "body"},
		{"trailing whitespace trimmed", "body \n\t\n", "body"},
		{"invalid utf8 becomes valid", "caf\xc3 bad", "caf\ufffd bad"},
		{"blank line between kept", "a\n\nb", "a\n\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, format.SafeBlock(tt.in))
		})
	}
}

// TestSafeBlock_SameRuneClassesAsSafeLine sweeps every Unicode scalar and
// proves SafeBlock and SafeLine strip, keep and convert exactly the same
// runes, so the two renderers cannot drift (MTIX-187.7.8, .19 share the rule).
func TestSafeBlock_SameRuneClassesAsSafeLine(t *testing.T) {
	for r := rune(0); r <= 0x10ffff; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		s := "a" + string(r) + "b"
		line := format.SafeLine(s)
		block := strings.Join(strings.Fields(strings.ReplaceAll(format.SafeBlock(s), "\n", " ")), " ")
		if got := strings.Join(strings.Fields(line), " "); got != block {
			t.Fatalf("rune %U: SafeLine %q, SafeBlock-as-line %q", r, line, block)
		}
	}
}

// TestSafeBlock_SafeLineIsBuiltOnIt proves SafeLine is SafeBlock with
// newlines folded to spaces, for every hostile fixture.
func TestSafeBlock_SafeLineIsBuiltOnIt(t *testing.T) {
	for _, f := range append(testutil.HostileText(), testutil.HostileIDs()...) {
		want := strings.TrimSpace(strings.ReplaceAll(format.SafeBlock(f.Value), "\n", " "))
		assert.Equal(t, want, format.SafeLine(f.Value), f.Name)
	}
}
