// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package redact_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/sync/redact"
)

// knownSecret mixes case and digits so it never occurs in ordinary text.
const knownSecret = "Kq8vZ3xW9mRt"

// TestKnown_RemovesExactDSNAndPassword_EvenWhenUnparseable pins the
// known-secret scrub (FR-18.17, MTIX-95.15): the exact DSN and the
// password it carries are removed from the text, whether or not the DSN
// parses as a URL, and whichever form the password takes in the text.
func TestKnown_RemovesExactDSNAndPassword_EvenWhenUnparseable(t *testing.T) {
	tests := []struct {
		name   string
		dsn    string
		leaked []string // forms of the secret that appear in the text
	}{
		{"well-formed URL",
			"postgres://u:" + knownSecret + "@hub.invalid:5432/mtix",
			[]string{knownSecret}},
		{"no scheme",
			"u:" + knownSecret + "@hub.invalid:5432/mtix",
			[]string{knownSecret}},
		{"invalid escape",
			"postgres://u:Kq8v%zzZ3xW@hub.invalid/mtix",
			[]string{"Kq8v%zzZ3xW"}},
		{"unescaped at-sign in password",
			"postgres://u:Kq8v@Z3xW@hub.invalid/mtix",
			[]string{"Kq8v@Z3xW", "Kq8v"}},
		{"unescaped slash in password",
			"postgres://u:Kq8v/Z3xW@hub.invalid/mtix",
			[]string{"Kq8v/Z3xW"}},
		{"at-sign in the query after the password",
			"postgres://u:" + knownSecret + "@hub.invalid/mtix?application_name=a@b",
			[]string{knownSecret}},
		{"percent-encoded password, decoded in the text",
			"postgres://u:Kq8v%40Z3xW@hub.invalid/mtix",
			[]string{"Kq8v%40Z3xW", "Kq8v@Z3xW"}},
		{"password with a character the URL form re-encodes",
			"postgres://u:Kq8v%20Z3xW@hub.invalid/mtix",
			[]string{"Kq8v%20Z3xW", "Kq8v Z3xW"}},
		{"password in the URL query, re-encoded in the text",
			"postgres://u@hub.invalid/mtix?password=Kq8v%20Z3xW&sslmode=verify-full",
			[]string{"Kq8v%20Z3xW", "Kq8v Z3xW", "Kq8v+Z3xW"}},
		{"keyword/value form",
			"host=hub.invalid user=u password=" + knownSecret + " dbname=mtix",
			[]string{knownSecret}},
		{"keyword/value form, quoted password",
			"host=hub.invalid user=u password='Kq8v Z3xW' dbname=mtix",
			[]string{"'Kq8v Z3xW'", "Kq8v Z3xW"}},
		{"keyword/value form, spaced equals sign",
			"host=hub.invalid PASSWORD = " + knownSecret,
			[]string{knownSecret}},
		{"surrounding whitespace, as read from a secrets file",
			"  postgres://u:" + knownSecret + "@hub.invalid/mtix\n",
			[]string{knownSecret, "postgres://u:" + knownSecret + "@hub.invalid/mtix"}},
		{"no password: the exact DSN is still removed",
			"postgres://hubuser@hub.invalid:6543/mtix?application_name=Kq8vZ3xW",
			nil},
		{"no password, surrounding whitespace",
			"\thubuser@hub.invalid/mtix?options=Kq8vZ3xW\n",
			[]string{"hubuser@hub.invalid/mtix?options=Kq8vZ3xW"}},
		{"plus sign kept by user-info decoding",
			"postgres://u:Kq8v+Z3%40xW@hub.invalid/mtix",
			[]string{"Kq8v+Z3%40xW", "Kq8v+Z3@xW"}},
		{"plus sign decoded to a space in the URL query",
			"postgres://u@hub.invalid/mtix?password=Kq8v+Z3xW",
			[]string{"Kq8v+Z3xW", "Kq8v Z3xW"}},
		{"lower-case escape re-encoded in upper case",
			"postgres://u:Kq8v%2fZ3xW@hub.invalid/mtix",
			[]string{"Kq8v%2fZ3xW", "Kq8v/Z3xW", "Kq8v%2FZ3xW"}},
		{"escapes the URL form rewrites",
			"postgres://u:Kq8v%3dZ3%2fxW@hub.invalid/mtix",
			[]string{"Kq8v%3dZ3%2fxW", "Kq8v=Z3/xW", "Kq8v=Z3%2FxW", "Kq8v%3DZ3%2FxW"}},
		{"password not first in the URL query",
			"postgres://u@hub.invalid/mtix?sslmode=verify-full&password=" + knownSecret,
			[]string{knownSecret}},
		{"keyword/value form, password first",
			"password=" + knownSecret + " host=hub.invalid",
			[]string{knownSecret}},
		{"keyword/value form, escaped quote in quoted password",
			`host=hub.invalid password='Kq8v\'Z3xW' dbname=mtix`,
			[]string{`'Kq8v\'Z3xW'`, "Kq8v'Z3xW"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, form := range append([]string{tt.dsn}, tt.leaked...) {
				in := "stage failed: [" + form + "] retrying"
				got := redact.Known(in, tt.dsn)
				require.NotContainsf(t, got, form, "form %q survived: %q", form, got)
				require.Contains(t, got, "stage failed: [", "surrounding text is kept")
				require.Contains(t, got, "] retrying", "surrounding text is kept")
				require.Contains(t, got, "REDACTED", "the secret is replaced by a visible marker")
			}
		})
	}
}

// TestKnown_URLShapedDSN_MaskedWithoutKnownValue: a DSN that is not a
// known value is still masked by its URL shape (FR-18.17, MTIX-95.15).
func TestKnown_URLShapedDSN_MaskedWithoutKnownValue(t *testing.T) {
	in := "connect: postgres://other:" + knownSecret + "@hub.invalid/mtix failed"
	got := redact.Known(in)
	require.NotContains(t, got, knownSecret)
	require.Contains(t, got, "postgres://REDACTED@hub.invalid/mtix")

	got = redact.Known(in, "", "   ")
	require.NotContains(t, got, knownSecret, "blank known values are ignored, URL masking still runs")
}

// TestKnown_LongestSecretRemovedFirst: when one candidate is a prefix
// of another (an unescaped '@' in the password), the whole password is
// removed as one unit, so no tail of it is left behind (MTIX-95.15).
func TestKnown_LongestSecretRemovedFirst(t *testing.T) {
	dsn := "postgres://u:Kq8v@Z3xW9mRt@hub.invalid/mtix"
	got := redact.Known("auth failed for Kq8v@Z3xW9mRt.", dsn)
	require.Equal(t, "auth failed for REDACTED.", got)
}

// TestKnown_NoSecret_TextUnchanged: text with no secret and no DSN
// shape passes through unchanged, and empty text stays empty.
func TestKnown_NoSecret_TextUnchanged(t *testing.T) {
	dsn := "postgres://u:" + knownSecret + "@hub.invalid/mtix"
	require.Equal(t, "", redact.Known("", dsn))
	require.Equal(t, "no DSN here", redact.Known("no DSN here", dsn))
	require.Equal(t, "user-only DSN", redact.Known("user-only DSN", "postgres://u@hub.invalid/mtix"),
		"a user name alone is not a secret")
	require.Equal(t, "text", redact.Known("text", "host=h password='"),
		"a lone quote as the password value is not a quoted value")
}
