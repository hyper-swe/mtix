// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/redact"
)

// specialPasswords are passwords with characters that break a raw URL
// DSN. Each must reach the driver byte for byte.
var specialPasswords = map[string]string{
	"slash":     "Zq9xab/cd",
	"question":  "Zq9xab?cd",
	"hash":      "Zq9xab#cd",
	"percent":   "Zq9xab%cd",
	"percent2":  "Zq9xab%25cd",
	"space":     "Zq9xab cd",
	"at":        "Zq9xab@cd",
	"colon":     "Zq9xab:cd",
	"quote":     "Zq9xab'cd",
	"backslash": `Zq9xab\cd`,
	"equals":    "Zq9xab=cd",
	"all":       `a/b?c#d%e f@g:h'i\j=k`,
}

// kvQuote writes pw as a libpq single-quoted value.
func kvQuote(pw string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(pw) + "'"
}

// approvedPassword approves dsn on a loopback host and returns the
// password the driver parsed.
func approvedPassword(t *testing.T, dsn string) string {
	t.Helper()
	a, err := transport.ApproveDSN(dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	return a.Config.ConnConfig.Password
}

func TestApproveDSN_SpecialCharPassword_KeyValueFormReachesDriverIntact(t *testing.T) {
	for name, pw := range specialPasswords {
		t.Run(name+"/quoted", func(t *testing.T) {
			dsn := "host=127.0.0.1 port=5432 user=mtix dbname=hub sslmode=disable password=" + kvQuote(pw)
			require.Equal(t, pw, approvedPassword(t, dsn))
		})
	}
	t.Run("unquoted without whitespace", func(t *testing.T) {
		dsn := "host=127.0.0.1 user=mtix sslmode=disable password=ab/cd?e#f%g@h:i"
		require.Equal(t, "ab/cd?e#f%g@h:i", approvedPassword(t, dsn))
	})
}

func TestApproveDSN_SpecialCharPassword_URLFormReachesDriverIntact(t *testing.T) {
	for name, pw := range specialPasswords {
		t.Run(name+"/encoded", func(t *testing.T) {
			u := url.URL{
				Scheme: "postgres", User: url.UserPassword("mtix", pw),
				Host: "127.0.0.1:5432", Path: "/hub", RawQuery: "sslmode=disable",
			}
			require.Equal(t, pw, approvedPassword(t, u.String()))
		})
	}
}

func TestApproveDSN_KeyValueForm_TLSPostureUnchanged(t *testing.T) {
	t.Run("sslmode defaults to verify-full", func(t *testing.T) {
		a, err := transport.ApproveDSN("host=hub.example.com user=mtix password=x", transport.Options{})
		require.NoError(t, err)
		require.Equal(t, "verify-full", a.SSLMode)
	})
	t.Run("weak sslmode needs the flag", func(t *testing.T) {
		_, err := transport.ApproveDSN("host=127.0.0.1 sslmode=disable password=x", transport.Options{})
		require.ErrorIs(t, err, transport.ErrTLSWeakWithoutFlag)
	})
	t.Run("weak sslmode needs a loopback host", func(t *testing.T) {
		_, err := transport.ApproveDSN("host=hub.example.com sslmode=require password=x",
			transport.Options{InsecureTLS: true})
		require.ErrorIs(t, err, transport.ErrTLSWeakNonLoopback)
	})
	t.Run("weak sslmode on loopback with the flag", func(t *testing.T) {
		a, err := transport.ApproveDSN("host=localhost sslmode=require password=x",
			transport.Options{InsecureTLS: true})
		require.NoError(t, err)
		require.Equal(t, "require", a.SSLMode)
	})
	t.Run("sslrootcert from the environment", func(t *testing.T) {
		ca, _ := writeTestCA(t)
		t.Setenv(transport.EnvSSLRootCert, ca)
		a, err := transport.ApproveDSN("host=hub.example.com user=mtix password=x", transport.Options{})
		require.NoError(t, err)
		require.Equal(t, ca, a.SSLRootCert)
		require.True(t, a.CASupplied)
	})
	t.Run("sslrootcert in the DSN wins", func(t *testing.T) {
		ca, _ := writeTestCA(t)
		other, _ := writeTestCA(t)
		t.Setenv(transport.EnvSSLRootCert, other)
		a, err := transport.ApproveDSN("host=hub.example.com password=x sslrootcert="+kvQuote(ca),
			transport.Options{})
		require.NoError(t, err)
		require.Equal(t, ca, a.SSLRootCert)
	})
}

func TestApproveDSN_SyncPasswordEnv_SuppliesPasswordOutOfBand(t *testing.T) {
	const pw = `p/w?#% @:'\`
	tests := []struct {
		name, dsn, want string
	}{
		{"url form without password", "postgres://mtix@127.0.0.1/hub?sslmode=disable", pw},
		{"key/value form without password", "host=127.0.0.1 user=mtix sslmode=disable", pw},
		{"password in url form wins", "postgres://mtix:inline@127.0.0.1/hub?sslmode=disable", "inline"},
		{"password in key/value form wins", "host=127.0.0.1 sslmode=disable password=inline", "inline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transport.EnvPassword, pw)
			require.Equal(t, tt.want, approvedPassword(t, tt.dsn))
		})
	}
}

func TestApproveDSN_Unparseable_ErrorNamesCauseWithoutCredential(t *testing.T) {
	const secret = "Kq8v/Z3xW9mRt"
	tests := []struct{ name, dsn string }{
		{"url with slash", "postgres://mtix:" + secret + "@hub.invalid/hub"},
		{"key/value unterminated quote", "host=hub.invalid password='" + secret},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := transport.ApproveDSN(tt.dsn, transport.Options{})
			require.ErrorIs(t, err, transport.ErrDSNMalformed)
			msg := err.Error()
			require.Contains(t, msg, "password")
			require.Contains(t, msg, "key=value")
			for _, w := range secretWindows(secret) {
				require.NotContains(t, msg, w)
			}
			require.NotContains(t, msg, "hub.invalid")
		})
	}
}

func TestEnforceTLSPosture_KeyValueForm_StaysKeyValueAndRoundTrips(t *testing.T) {
	const pw = `a/b?c#d%e f@g:h'i\j=k`
	out, err := transport.EnforceTLSPosture("host=127.0.0.1 sslmode=disable password="+kvQuote(pw),
		transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	require.NotContains(t, out, "://")
	require.Equal(t, pw, approvedPassword(t, out))
}

func TestKnown_KeyValueDSN_RemovesSpecialCharPassword(t *testing.T) {
	for name, pw := range specialPasswords {
		t.Run(name, func(t *testing.T) {
			dsn := "host=hub.example.com user=mtix password=" + kvQuote(pw)
			got := redact.Known("connect failed: "+dsn+" and "+pw, dsn)
			require.NotContains(t, got, pw)
			require.NotContains(t, got, dsn)
		})
	}
}

// TestApproveDSN_KeyValuePasswordHoldingScheme_Accepted verifies a key=value
// DSN whose password contains :// is accepted: the scheme guard applies to
// text that is not a key=value DSN (MTIX-87).
func TestApproveDSN_KeyValuePasswordHoldingScheme_Accepted(t *testing.T) {
	const pw = "ab://cd"
	dsn := "host=127.0.0.1 port=5432 dbname=hub user=mtix sslmode=disable password=" + kvQuote(pw)
	require.Equal(t, pw, approvedPassword(t, dsn))
	_, err := transport.ApproveDSN("mysql://u:p@127.0.0.1/hub", transport.Options{InsecureTLS: true})
	require.ErrorIs(t, err, transport.ErrDSNMalformed, "a foreign scheme is still refused")
}

// TestApproveDSN_DuplicateKeys_LastWins verifies the last of two equal
// settings is the one the approval reports and the driver reads, as libpq
// reads them (MTIX-87).
func TestApproveDSN_DuplicateKeys_LastWins(t *testing.T) {
	first, _ := writeTestCA(t)
	last, _ := writeTestCA(t)
	dsn := "host=hub.example.com dbname=hub user=mtix password=first password=second" +
		" sslrootcert=" + kvQuote(first) + " sslrootcert=" + kvQuote(last)
	a, err := transport.ApproveDSN(dsn, transport.Options{})
	require.NoError(t, err)
	require.Equal(t, last, a.SSLRootCert, "the last sslrootcert is the one reported")
	require.Equal(t, "second", a.Config.ConnConfig.Password)
}

// TestApproveDSN_SpacesAroundEquals_Accepted verifies a key=value DSN with
// white space around each equals sign, a form libpq accepts, is approved
// and keeps its values byte for byte (MTIX-87).
func TestApproveDSN_SpacesAroundEquals_Accepted(t *testing.T) {
	dsn := "host = 127.0.0.1 port = 5432 dbname = hub user = mtix sslmode = disable password = " + kvQuote("p w/?#%")
	require.Equal(t, "p w/?#%", approvedPassword(t, dsn))
	a, err := transport.ApproveDSN(dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	require.Equal(t, "disable", a.SSLMode)
}
