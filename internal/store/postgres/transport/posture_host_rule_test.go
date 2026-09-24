// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"crypto/tls"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// verifiedEntry is a connection attempt at host that verifies the
// server certificate against host, as the driver builds it under
// verify-full.
func verifiedEntry(host string) *pgconn.FallbackConfig {
	return &pgconn.FallbackConfig{Host: host, Port: 5432, TLSConfig: &tls.Config{ServerName: host}}
}

func TestCheckHostEntries_VerifyFull_AcceptsOnlyVerifyingNetworkEntries(t *testing.T) {
	const hidden = "hidden-host.example"
	tests := []struct {
		name    string
		entries []*pgconn.FallbackConfig
		wantPos string
	}{
		{"every network entry verifies", []*pgconn.FallbackConfig{verifiedEntry("a.example"), verifiedEntry(hidden)}, ""},
		{"local socket entry without TLS", []*pgconn.FallbackConfig{{Host: "/tmp", Port: 5432}, verifiedEntry(hidden)}, ""},
		{"network entry without TLS", []*pgconn.FallbackConfig{verifiedEntry("a.example"), {Host: hidden, Port: 5432}},
			"host 2 of 2 "},
		{"entry that skips certificate verification", []*pgconn.FallbackConfig{
			{Host: hidden, Port: 5432, TLSConfig: &tls.Config{ServerName: hidden, InsecureSkipVerify: true}}}, "host 1 of 1 "},
		{"entry that verifies another name", []*pgconn.FallbackConfig{
			{Host: hidden, Port: 5432, TLSConfig: &tls.Config{ServerName: "other.example"}}}, "host 1 of 1 "},
		{"entry without a server name", []*pgconn.FallbackConfig{
			verifiedEntry("a.example"), verifiedEntry("b.example"), {Host: hidden, Port: 5432, TLSConfig: &tls.Config{}}},
			"host 3 of 3 "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkHostEntries(sslModeVerifyFull, tt.entries)
			if tt.wantPos == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrTLSUnverified), "want ErrTLSUnverified, got %v", err)
			require.Contains(t, err.Error(), tt.wantPos)
			require.NotContains(t, err.Error(), hidden, "message must not echo host text")
			require.NotContains(t, err.Error(), "other.example", "message must not echo host text")
		})
	}
}

func TestCheckHostEntries_WeakMode_RefusesNonLocalEntryByHostPosition(t *testing.T) {
	const hidden = "hidden-host.example"
	// tlsOn models the TLS entry the driver builds under allow and prefer.
	tlsOn := &tls.Config{InsecureSkipVerify: true}
	tests := []struct {
		name    string
		mode    string
		entries []*pgconn.FallbackConfig
		wantPos string
	}{
		{"loopback and socket entries", "disable", []*pgconn.FallbackConfig{
			{Host: "localhost", Port: 5432}, {Host: "::1", Port: 5432}, {Host: "/tmp", Port: 5432}}, ""},
		{"remote entry", "disable", []*pgconn.FallbackConfig{{Host: "localhost", Port: 5432}, {Host: hidden, Port: 5432}},
			"host 2 of 2 "},
		{"prefer lists each host twice", "prefer", []*pgconn.FallbackConfig{
			{Host: "localhost", Port: 5432, TLSConfig: tlsOn}, {Host: "localhost", Port: 5432},
			{Host: hidden, Port: 5432, TLSConfig: tlsOn}, {Host: hidden, Port: 5432}}, "host 2 of 2 "},
		{"allow lists each host twice", "allow", []*pgconn.FallbackConfig{
			{Host: hidden, Port: 5432}, {Host: hidden, Port: 5432, TLSConfig: tlsOn},
			{Host: "localhost", Port: 5432}, {Host: "localhost", Port: 5432, TLSConfig: tlsOn}}, "host 1 of 2 "},
		{"same host on two ports", "disable", []*pgconn.FallbackConfig{
			{Host: "localhost", Port: 5432}, {Host: hidden, Port: 5432}, {Host: hidden, Port: 5433}}, "host 2 of 3 "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkHostEntries(tt.mode, tt.entries)
			if tt.wantPos == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrTLSWeakNonLoopback), "want ErrTLSWeakNonLoopback, got %v", err)
			require.Contains(t, err.Error(), tt.wantPos)
			require.NotContains(t, err.Error(), hidden, "message must not echo host text")
		})
	}
}

func TestSSLModeLabel_KnownAndUnknownModes_NamesOnlyKnownModes(t *testing.T) {
	tests := []struct {
		mode string
		want string
	}{
		{"disable", "disable"},
		{"allow", "allow"},
		{"prefer", "prefer"},
		{"require", "require"},
		{"verify-ca", "verify-ca"},
		{"verify-full", "verify-full"},
		{"", "(unrecognized)"},
		{"s3cret", "(unrecognized)"},
		{"verify-full ", "(unrecognized)"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			require.Equal(t, tt.want, sslModeLabel(tt.mode))
		})
	}
}
