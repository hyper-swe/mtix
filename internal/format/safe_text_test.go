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

func TestSafeText_Cases_EscapeUnsafeKeepPrintable(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "node TEST-1.2: not found", "node TEST-1.2: not found"},
		{"printable non-ASCII kept", "caf\u00e9 \u4e16\u754c \u03b1", "caf\u00e9 \u4e16\u754c \u03b1"},
		{"NUL", "a\x00b", `a\x00b`},
		{"ESC CSI", "\x1b[31mred", `\x1b[31mred`},
		{"newline tab CR", "a\nb\tc\rd", `a\nb\tc\rd`},
		{"C1", "a\u0085b", `a\x85b`},
		{"bidi override", "a\u202eb", "a\\u202eb"},
		{"zero width", "a\u200bb", "a\\u200bb"},
		{"line separator", "a\u2028b", "a\\u2028b"},
		{"soft hyphen (Cf)", "a\u00adb", `a\xadb`},
		{"Hangul filler", "a\u3164b", "a\\u3164b"},
		{"combining grapheme joiner", "a\u034fb", "a\\u034fb"},
		{"tag char", "a\U000e0041b", `a\U000e0041b`},
		{"invalid UTF-8", "a\xffb", `a\xffb`},
		{"directional marks kept", "a\u200eb\u200fc\u061cd", "a\u200eb\u200fc\u061cd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, format.SafeText(tc.in))
		})
	}
}

func TestSafeText_HostileIDs_PrintableValidAndIdempotent(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		t.Run(f.Name, func(t *testing.T) {
			got := format.SafeText("node " + f.Value + ": not found")
			require.True(t, utf8.ValidString(got))
			for _, r := range got {
				switch {
				case r == '\u200e' || r == '\u200f' || r == '\u061c':
				case unicode.Is(unicode.Cf, r), !unicode.IsPrint(r) && !unicode.IsSpace(r):
					t.Fatalf("unsafe rune %U survives in %q", r, got)
				}
			}
			assert.Equal(t, got, format.SafeText(got), "escaping twice changes nothing")
			assert.False(t, strings.ContainsAny(got, "\x00\x1b\n\r"))
		})
	}
}
