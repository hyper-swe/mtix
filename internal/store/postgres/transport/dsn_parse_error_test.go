// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// The fixed messages a DSN that cannot be parsed produces (FR-18.17,
// MTIX-95.15). EnforceTLSPosture prefixes them with "parse dsn: ".
const (
	wantSchemeMessage = "parse dsn: DSN could not be parsed: it must start with postgres:// or postgresql://"
	wantURLMessage    = "parse dsn: DSN could not be parsed: percent-encode reserved characters in the user name and password"
)

// parseErrorUser, parseErrorSecret and parseErrorHost are the synthetic
// DSN parts no parse error may repeat. The secret mixes case and digits
// so none of its 3-character windows occurs in ordinary message text.
const (
	parseErrorUser   = "parseuser"
	parseErrorSecret = "Kq8vZ3xW9mRt"
	parseErrorHost   = "hub.invalid"
)

// secretWindows returns every 3-character substring of secret, so a
// test can prove no fragment of it survives, not only the whole value.
func secretWindows(secret string) []string {
	const width = 3
	var out []string
	for i := 0; i+width <= len(secret); i++ {
		out = append(out, secret[i:i+width])
	}
	return out
}

// TestDSNParseError_NoSecretFragment pins the parse-error contract of
// parseDSN, reached through EnforceTLSPosture: a DSN that fails to parse
// yields one fixed message per failure class, and no part of the DSN
// (user, password or any 3-character window of it, host) appears in it
// (FR-18.17, MTIX-95.15).
func TestDSNParseError_NoSecretFragment(t *testing.T) {
	tests := []struct {
		name     string
		dsn      string
		password string
		want     string
	}{
		{"no scheme, URL-shaped",
			parseErrorUser + ":" + parseErrorSecret + "@" + parseErrorHost + ":5432/mtix",
			parseErrorSecret, wantSchemeMessage},
		{"no scheme, password first",
			parseErrorSecret + "@" + parseErrorHost,
			parseErrorSecret, wantSchemeMessage},
		{"no scheme, keyword/value form",
			"host=" + parseErrorHost + " user=" + parseErrorUser + " password=" + parseErrorSecret,
			parseErrorSecret, wantSchemeMessage},
		{"unsupported scheme",
			"mysql://" + parseErrorUser + ":" + parseErrorSecret + "@" + parseErrorHost + "/mtix",
			parseErrorSecret, wantSchemeMessage},
		{"invalid escape in password",
			"postgres://" + parseErrorUser + ":Kq8v%zzZ3xW9mRt@" + parseErrorHost + "/mtix",
			"Kq8v%zzZ3xW9mRt", wantURLMessage},
		{"space in password",
			"postgresql://" + parseErrorUser + ":Kq8v Z3xW9mRt@" + parseErrorHost + "/mtix",
			"Kq8v Z3xW9mRt", wantURLMessage},
		{"unescaped slash in password",
			"postgres://" + parseErrorUser + ":Kq8v/Z3xW9mRt@" + parseErrorHost + "/mtix",
			"Kq8v/Z3xW9mRt", wantURLMessage},
		{"control character in password",
			"postgres://" + parseErrorUser + ":Kq8v\x7fZ3xW9mRt@" + parseErrorHost + "/mtix",
			"Kq8v\x7fZ3xW9mRt", wantURLMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, opts := range []transport.Options{{}, {InsecureTLS: true}} {
				out, err := transport.EnforceTLSPosture(tt.dsn, opts)
				require.Error(t, err)
				require.Empty(t, out)
				msg := err.Error()
				require.Equal(t, tt.want, msg, "a parse failure has one fixed message")
				require.ErrorIs(t, err, transport.ErrDSNMalformed)
				require.NotContains(t, msg, parseErrorUser)
				require.NotContains(t, msg, parseErrorHost)
				for _, w := range secretWindows(tt.password) {
					require.NotContainsf(t, msg, w, "message repeats password fragment %q", w)
				}
			}
		})
	}
}
