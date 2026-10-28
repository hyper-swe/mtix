// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestHostileIDs_CoversEveryClass(t *testing.T) {
	assertCoversEveryClass(t, HostileIDs())
}

func assertCoversEveryClass(t *testing.T, fixtures []HostileFixture) {
	t.Helper()
	seen := map[HostileClass]int{}
	for _, f := range fixtures {
		require.NotEmpty(t, f.Name)
		require.NotEmpty(t, f.Class, f.Name)
		seen[f.Class]++
	}
	require.NotEmpty(t, AllHostileClasses())
	for _, c := range AllHostileClasses() {
		require.NotZero(t, seen[c], "class %s has no fixture", c)
	}
	for c := range seen {
		require.Contains(t, AllHostileClasses(), c)
	}
}

func TestHostileText_CoversEveryClass(t *testing.T) {
	assertCoversEveryClass(t, HostileText())
}

func TestHostileIDs_AreStableAndDistinct(t *testing.T) {
	assertStableAndDistinct(t, HostileIDs)
}

func TestHostileText_AreStableAndDistinct(t *testing.T) {
	assertStableAndDistinct(t, HostileText)
}

func assertStableAndDistinct(t *testing.T, gen func() []HostileFixture) {
	t.Helper()
	first := gen()
	names := map[string]bool{}
	values := map[string]bool{}
	for _, f := range first {
		require.False(t, names[f.Name], "duplicate name %q", f.Name)
		require.False(t, values[f.Value], "duplicate value for %q", f.Name)
		require.NotEmpty(t, f.Value, f.Name)
		names[f.Name], values[f.Value] = true, true
	}
	want := gen()
	// Mutate the returned slice; later calls must be unaffected.
	first[0].Value = "mutated"
	first[0].Name = "mutated"
	got := gen()
	require.Equal(t, want, got)
	require.Len(t, got, len(first))
}

// classPredicates proves each fixture really carries its class payload, so
// a fixture made benign (or mislabelled) fails.
var classPredicates = map[HostileClass]func(string) bool{
	ClassNUL:  func(s string) bool { return strings.Contains(s, "\x00") },
	ClassESC:  func(s string) bool { return strings.ContainsAny(s, "\x1b\u009b") },
	ClassCRLF: func(s string) bool { return strings.ContainsAny(s, "\r\n") },
	ClassBidi: func(s string) bool {
		return strings.ContainsAny(s, "\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069")
	},
	ClassZeroWidth:   func(s string) bool { return strings.ContainsAny(s, "\u200b\u200c\u200d\u2060\ufeff") },
	ClassInvalidUTF8: func(s string) bool { return !utf8.ValidString(s) },
	ClassLong:        func(s string) bool { return len(s) > 64*1024 },
	ClassTraversal:   func(s string) bool { return strings.Contains(s, "..") },
	ClassSlash:       func(s string) bool { return strings.Contains(s, "/") },
	ClassBackslash:   func(s string) bool { return strings.Contains(s, `\`) },
	ClassShell:       func(s string) bool { return strings.ContainsAny(s, "$`;|&") },
	ClassQuote:       func(s string) bool { return strings.ContainsAny(s, `'"`) },
	ClassScript: func(s string) bool {
		return strings.Contains(strings.ToLower(s), "<script") || strings.Contains(strings.ToLower(s), "onerror=")
	},
	ClassJSURI:       func(s string) bool { return strings.Contains(strings.ToLower(s), "javascript:") },
	ClassJSONBreaker: func(s string) bool { return strings.ContainsAny(s, `"}`) || strings.Contains(s, "</script>") },
	ClassWindowsReserved: func(s string) bool {
		for _, w := range strings.Fields(s) {
			base := strings.ToUpper(strings.SplitN(w, ".", 2)[0])
			switch base {
			case "CON", "PRN", "AUX", "NUL", "COM1", "COM9", "LPT1":
				return true
			}
		}
		return false
	},
	ClassLeadingDash:  func(s string) bool { return strings.HasPrefix(s, "-") },
	ClassLikeWildcard: func(s string) bool { return strings.ContainsAny(s, "%_") },
	ClassControl:      func(s string) bool { return strings.ContainsAny(s, "\t\x7f\u0085\u0090") },
	ClassDot:          func(s string) bool { return s == "." },
	ClassUnicodeForm: func(s string) bool {
		return strings.Contains(s, "\u00e9") || strings.Contains(s, "e\u0301") || strings.Contains(s, "\u212a") || strings.Contains(s, "\u0130")
	},
}

func TestHostileFixtures_PayloadMatchesClass(t *testing.T) {
	require.Len(t, classPredicates, len(AllHostileClasses()))
	for label, fixtures := range map[string][]HostileFixture{"ids": HostileIDs(), "text": HostileText()} {
		for _, f := range fixtures {
			pred, ok := classPredicates[f.Class]
			require.True(t, ok, "%s/%s: unknown class %s", label, f.Name, f.Class)
			require.True(t, pred(f.Value), "%s/%s is not a %s payload", label, f.Name, f.Class)
		}
	}
}

var requiredIDNames = []string{
	"nul_middle",
	"nul_only",
	"esc_csi_color",
	"osc_title",
	"osc8_link",
	"c1_csi",
	"crlf_injection",
	"cr_overwrite",
	"bidi_rlo",
	"bidi_isolate",
	"zero_width_space",
	"zero_width_joiner_bom",
	"invalid_utf8_lone_continuation",
	"invalid_utf8_truncated",
	"long_64kib_plus",
	"dotdot_relative",
	"dotdot_only",
	"slash_absolute",
	"slash_embedded",
	"backslash_windows_path",
	"backslash_embedded",
	"shell_subshell",
	"shell_backtick_semicolon",
	"quote_single_breakout",
	"quote_double_breakout",
	"script_tag",
	"script_img_onerror",
	"javascript_uri",
	"javascript_uri_mixed_case",
	"json_quote_brace",
	"json_script_close",
	"windows_reserved_con",
	"windows_reserved_nul_ext",
	"windows_reserved_com1",
	"leading_dash_flag",
	"leading_dash_short",
	"like_percent",
	"like_underscore",
	"control_tab",
	"control_del",
	"control_c1_nel",
	"single_dot",
	"unicode_nfc_e_acute",
	"unicode_nfd_e_acute",
	"unicode_kelvin_sign",
	"unicode_dotted_capital_i",
}

var requiredTextNames = []string{
	"nul_in_title",
	"esc_clear_screen",
	"osc_set_title",
	"crlf_forged_header",
	"cr_spoof",
	"bidi_override",
	"zero_width_split",
	"invalid_utf8_bytes",
	"long_64kib_plus",
	"dotdot_in_text",
	"slash_in_text",
	"backslash_in_text",
	"shell_in_text",
	"quotes_in_text",
	"script_in_text",
	"javascript_link",
	"json_breaker_text",
	"windows_reserved_text",
	"leading_dash_text",
	"like_wildcards_text",
	"control_chars_text",
	"single_dot_text",
	"unicode_forms_text",
}

func TestHostileFixtures_RequiredNamesPresent(t *testing.T) {
	ids := map[string]bool{}
	for _, f := range HostileIDs() {
		ids[f.Name] = true
	}
	for _, n := range requiredIDNames {
		require.True(t, ids[n], "HostileIDs lost fixture %s", n)
	}
	text := map[string]bool{}
	for _, f := range HostileText() {
		text[f.Name] = true
	}
	for _, n := range requiredTextNames {
		require.True(t, text[n], "HostileText lost fixture %s", n)
	}
}

func TestHostileFixtures_ExactValues(t *testing.T) {
	byName := map[string]string{}
	for _, f := range HostileIDs() {
		byName[f.Name] = f.Value
	}
	require.Equal(t, "\x00", byName["nul_only"])
	require.Equal(t, "CON", byName["windows_reserved_con"])
	require.Equal(t, "JaVaScRiPt:alert(1)", byName["javascript_uri_mixed_case"])
	require.Equal(t, "$(touch pwned)", byName["shell_subshell"])
	require.Equal(t, "evt' OR '1'='1", byName["quote_single_breakout"])
	require.Equal(t, "evt\u202etxt.exe", byName["bidi_rlo"])
	require.Equal(t, ".", byName["single_dot"])
	require.NotEqual(t, byName["unicode_nfc_e_acute"], byName["unicode_nfd_e_acute"])
}

func TestHostileIDs_LongFixtureExceeds64KiB(t *testing.T) {
	for _, f := range HostileIDs() {
		if f.Class == ClassLong {
			require.Greater(t, len(f.Value), 64*1024)
			return
		}
	}
	t.Fatal("no long ID fixture")
}

func TestHostileText_Has64KiBFixture(t *testing.T) {
	for _, f := range HostileText() {
		if f.Class == ClassLong && len(f.Value) > 64*1024 {
			return
		}
	}
	t.Fatal("no >64KiB text fixture")
}

func TestHostileFixtures_ImportStdlibOnly(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "hostile.go", nil, parser.ImportsOnly)
	require.NoError(t, err)
	for _, imp := range f.Imports {
		p, uerr := strconv.Unquote(imp.Path.Value)
		require.NoError(t, uerr)
		require.False(t, strings.Contains(strings.Split(p, "/")[0], "."), "non-stdlib import %s", p)
	}
}

func TestTestutil_NoProductionImporters(t *testing.T) {
	root := filepath.Join("..", "..")
	self := filepath.Join(root, "internal", "testutil")
	var offenders []string
	walk := func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if p == self || info.Name() == "node_modules" || info.Name() == ".git" || info.Name() == ".hyperswe" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if perr != nil {
			return nil //nolint:nilerr // unparsable fixtures are not production sources
		}
		for _, imp := range f.Imports {
			if strings.HasSuffix(imp.Path.Value, "mtix/internal/testutil\"") {
				offenders = append(offenders, p)
			}
		}
		return nil
	}
	require.NoError(t, filepath.Walk(root, walk))
	require.Empty(t, offenders)
}
