// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// weakModes lists every sslmode weaker than verify-full that the
// driver accepts. Each host-rule case runs under all of them.
var weakModes = []string{"disable", "allow", "prefer", "require", "verify-ca"}

// pinPGEnv clears every PG* variable the driver's parser merges into
// the connection settings, so each case sees only the settings it
// states (FR-18.15, MTIX-95.20).
func pinPGEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"PGHOST", "PGPORT", "PGSERVICE", "PGSERVICEFILE", "PGSSLMODE",
		"PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY", "PGSSLNEGOTIATION",
		"PGCONNECT_TIMEOUT", "PGTARGETSESSIONATTRS",
		"PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION",
		transport.EnvSSLRootCert,
	} {
		t.Setenv(k, "")
	}
}

// writeServiceFile writes a connection service file defining one
// service named "hub" with the given host value and returns its path.
func writeServiceFile(t *testing.T, host string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pg_service.conf")
	body := "[hub]\nhost=" + host + "\ndbname=hub\n"
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

// withSSLMode appends sslmode=mode to dsn.
func withSSLMode(dsn, mode string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "sslmode=" + mode
}

// hostCase is one DSN form for the host rule. serviceHost, when set,
// is written to a service file whose path is exported as
// PGSERVICEFILE; "{servicefile}" in dsn is replaced by that path.
type hostCase struct {
	name        string
	dsn         string
	env         map[string]string
	serviceHost string
}

// applyHostCase pins the environment for tc and returns its DSN.
func applyHostCase(t *testing.T, tc hostCase) string {
	t.Helper()
	pinPGEnv(t)
	dsn := tc.dsn
	if tc.serviceHost != "" {
		p := writeServiceFile(t, tc.serviceHost)
		t.Setenv("PGSERVICEFILE", p)
		dsn = strings.ReplaceAll(dsn, "{servicefile}", url.QueryEscape(p))
	}
	for k, v := range tc.env {
		t.Setenv(k, v)
	}
	return dsn
}

func TestEnforceTLSPosture_NonLoopbackEffectiveHost_ReturnsErrTLSWeakNonLoopback(t *testing.T) {
	cases := []hostCase{
		{name: "remote authority host", dsn: "postgres://u:pw@db.example.com/hub"},
		{name: "host parameter with empty authority", dsn: "postgres://u:pw@/hub?host=db.example.com"},
		{name: "host parameter overrides loopback authority", dsn: "postgres://u:pw@localhost/hub?host=db.example.com"},
		{name: "host parameter list with remote second entry", dsn: "postgres://u:pw@/hub?host=localhost,db.example.com"},
		{name: "host parameter list with remote first entry", dsn: "postgres://u:pw@/hub?host=db.example.com,127.0.0.1"},
		{name: "host parameter list with unix socket and remote entry", dsn: "postgres://u:pw@/hub?host=/tmp,db.example.com"},
		{name: "empty host parameter", dsn: "postgres://u:pw@/hub?host="},
		{name: "empty entry in host parameter list", dsn: "postgres://u:pw@/hub?host=localhost,,127.0.0.1"},
		{name: "authority list with remote fallback", dsn: "postgres://u:pw@localhost:5432,db.example.com:5432/hub"},
		{name: "authority list without ports", dsn: "postgres://u:pw@localhost,db.example.com/hub"},
		{name: "authority list of addresses with remote fallback", dsn: "postgres://u:pw@127.0.0.1:5432,10.0.0.5:5432/hub"},
		{name: "unbracketed IPv6 literal with port", dsn: "postgres://u:pw@::1:5432/hub"},
		{name: "unspecified IPv4 address", dsn: "postgres://u:pw@0.0.0.0/hub"},
		{name: "unspecified IPv6 address", dsn: "postgres://u:pw@[::]/hub"},
		{name: "name that only starts with localhost", dsn: "postgres://u:pw@localhost.example.com/hub"},
		{name: "PGHOST with empty authority", dsn: "postgres://u:pw@/hub",
			env: map[string]string{"PGHOST": "db.example.com"}},
		{name: "PGHOST list with remote second entry", dsn: "postgres://u:pw@/hub",
			env: map[string]string{"PGHOST": "localhost,db.example.com"}},
		{name: "service named in the DSN", dsn: "postgres://u:pw@/hub?service=hub",
			serviceHost: "db.example.com"},
		{name: "service named by PGSERVICE", dsn: "postgres://u:pw@/hub",
			env: map[string]string{"PGSERVICE": "hub"}, serviceHost: "db.example.com"},
		{name: "service file named in the DSN", dsn: "postgres://u:pw@/hub?service=hub&servicefile={servicefile}",
			serviceHost: "db.example.com"},
		{name: "service host list with remote fallback", dsn: "postgres://u:pw@/hub?service=hub",
			serviceHost: "localhost,db.example.com"},
	}
	for _, tc := range cases {
		for _, mode := range weakModes {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				dsn := withSSLMode(applyHostCase(t, tc), mode)
				out, err := transport.EnforceTLSPosture(dsn, transport.Options{InsecureTLS: true})
				require.Error(t, err)
				require.True(t, errors.Is(err, transport.ErrTLSWeakNonLoopback),
					"want ErrTLSWeakNonLoopback, got %v", err)
				require.Empty(t, out)
			})
		}
	}
}

func TestEnforceTLSPosture_NonLoopbackHost_MessageNamesPositionNotHost(t *testing.T) {
	cases := []struct {
		hostCase
		hostText string
		position string
	}{
		{hostCase{name: "second entry of an authority list",
			dsn: "postgres://u:pw@localhost:12,secret:34/x@h/db"}, "secret", "host 2 of 2 "},
		{hostCase{name: "second entry of a host parameter list",
			dsn: "postgres://u:pw@/hub?host=localhost,db.example.com"}, "db.example.com", "host 2 of 2 "},
		{hostCase{name: "third entry of a host parameter list",
			dsn: "postgres://u:pw@/hub?host=localhost,/tmp,db.example.com"}, "db.example.com", "host 3 of 3 "},
		{hostCase{name: "PGHOST", dsn: "postgres://u:pw@/hub",
			env: map[string]string{"PGHOST": "env-host.example.com"}}, "env-host.example.com", "host 1 of 1 "},
		{hostCase{name: "service file", dsn: "postgres://u:pw@/hub?service=hub",
			serviceHost: "svc-host.example.com"}, "svc-host.example.com", "host 1 of 1 "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := withSSLMode(applyHostCase(t, tc.hostCase), "disable")
			_, err := transport.EnforceTLSPosture(dsn, transport.Options{InsecureTLS: true})
			require.Error(t, err)
			require.True(t, errors.Is(err, transport.ErrTLSWeakNonLoopback))
			require.NotContains(t, err.Error(), tc.hostText, "message must not echo host text")
			require.Contains(t, err.Error(), tc.position)
		})
	}
}

func TestEnforceTLSPosture_WeakModeWithoutFlag_MessageNamesNoHost(t *testing.T) {
	tests := []struct {
		name     string
		dsn      string
		hostText string
	}{
		{"password fragment read as host", "postgres://u:p@ss:1/x@h/db?sslmode=disable", `"ss"`},
		{"password fragment in a host list", "postgres://u:pw@localhost:12,secret:34/x@h/db?sslmode=disable", "secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinPGEnv(t)
			_, err := transport.EnforceTLSPosture(tt.dsn, transport.Options{})
			require.Error(t, err)
			require.True(t, errors.Is(err, transport.ErrTLSWeakWithoutFlag))
			require.NotContains(t, err.Error(), tt.hostText, "message must not echo host text")
			require.NotContains(t, err.Error(), "host", "message must not name a host")
		})
	}
}

func TestErrTLSWeakNonLoopback_Text_NamesLoopbackAndLocalSockets(t *testing.T) {
	require.Equal(t, "weak TLS only allowed on loopback hosts or local sockets",
		transport.ErrTLSWeakNonLoopback.Error())
}

// localHostCases lists the DSN forms whose every host is loopback or a
// local socket, so each is accepted under every weak sslmode.
func localHostCases() []hostCase {
	return []hostCase{
		{name: "localhost", dsn: "postgres://u:pw@localhost:5432/hub"},
		{name: "localhost in mixed case", dsn: "postgres://u:pw@LocalHost/hub"},
		{name: "IPv4 loopback", dsn: "postgres://u:pw@127.0.0.1:5432/hub"},
		{name: "other 127/8 address", dsn: "postgres://u:pw@127.0.0.2/hub"},
		{name: "bracketed IPv6 loopback with port", dsn: "postgres://u:pw@[::1]:5432/hub"},
		{name: "bracketed IPv6 loopback", dsn: "postgres://u:pw@[::1]/hub"},
		{name: "IPv4-mapped IPv6 loopback", dsn: "postgres://u:pw@[::ffff:127.0.0.1]/hub"},
		{name: "authority list of loopback hosts", dsn: "postgres://u:pw@localhost:5432,127.0.0.1:5433/hub"},
		{name: "loopback host parameter", dsn: "postgres://u:pw@/hub?host=127.0.0.1"},
		{name: "host parameter list of local hosts", dsn: "postgres://u:pw@/hub?host=localhost,::1,/var/run/postgresql"},
		{name: "unix socket host parameter", dsn: "postgres://u:pw@/hub?host=/var/run/postgresql"},
		{name: "unix socket from PGHOST", dsn: "postgres://u:pw@/hub",
			env: map[string]string{"PGHOST": "/tmp"}},
		{name: "loopback PGHOST", dsn: "postgres://u:pw@/hub",
			env: map[string]string{"PGHOST": "127.0.0.1"}},
		{name: "authority host takes precedence over PGHOST", dsn: "postgres://u:pw@localhost/hub",
			env: map[string]string{"PGHOST": "db.example.com"}},
		{name: "host parameter takes precedence over PGHOST", dsn: "postgres://u:pw@/hub?host=::1",
			env: map[string]string{"PGHOST": "db.example.com"}},
		{name: "loopback service host", dsn: "postgres://u:pw@/hub?service=hub",
			serviceHost: "localhost"},
		{name: "driver default host", dsn: "postgres://u:pw@/hub"},
	}
}

func TestEnforceTLSPosture_LocalEffectiveHosts_AcceptsWeakMode(t *testing.T) {
	cases := localHostCases()
	for _, tc := range cases {
		for _, mode := range weakModes {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				dsn := withSSLMode(applyHostCase(t, tc), mode)
				out, err := transport.EnforceTLSPosture(dsn, transport.Options{InsecureTLS: true})
				require.NoError(t, err)
				u, perr := url.Parse(out)
				require.NoError(t, perr)
				require.Equal(t, mode, u.Query().Get("sslmode"), "caller's sslmode is kept")
			})
		}
	}
}

func TestEnforceTLSPosture_LoopbackHostWithCertificateSettings_ReturnsDSNUnchanged(t *testing.T) {
	pinPGEnv(t)
	// Certificate settings from the environment are loaded by the check's
	// one parse, so they name real files; they never change the returned
	// DSN (MTIX-95.25).
	envCA, _ := writeTestCA(t)
	explicitCA, _ := writeTestCA(t)
	clientCert, clientKey := writeTestClientCert(t)
	t.Setenv("PGSSLROOTCERT", envCA)
	t.Setenv("PGSSLCERT", clientCert)
	t.Setenv("PGSSLKEY", clientKey)

	tests := []struct {
		name         string
		dsn          string
		wantRootCert []string
	}{
		{"explicit sslrootcert is kept",
			"postgres://u:pw@localhost/hub?sslmode=require&sslrootcert=" + url.QueryEscape(explicitCA),
			[]string{explicitCA}},
		{"no sslrootcert is added",
			"postgres://u:pw@localhost/hub?sslmode=require", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := transport.EnforceTLSPosture(tt.dsn, transport.Options{InsecureTLS: true})
			require.NoError(t, err)
			u, perr := url.Parse(out)
			require.NoError(t, perr)
			q := u.Query()
			require.Equal(t, []string{"require"}, q["sslmode"])
			require.Equal(t, tt.wantRootCert, q["sslrootcert"])
		})
	}
}

func TestEnforceTLSPosture_DSNTheDriverCannotParse_ReturnsFixedMessage(t *testing.T) {
	const user, secret = "mtixuser", "S3cretPw"
	tests := []struct {
		name string
		dsn  string
	}{
		{"invalid connect_timeout on loopback host",
			"postgres://" + user + ":" + secret + "@localhost/hub?connect_timeout=bogus"},
		{"invalid connect_timeout on remote host",
			"postgres://" + user + ":" + secret + "@db.example.com/hub?connect_timeout=bogus"},
		{"invalid port list",
			"postgres://" + user + ":" + secret + "@/hub?host=localhost&port=notaport"},
		{"unknown target_session_attrs",
			"postgres://" + user + ":" + secret + "@localhost/hub?target_session_attrs=bogus"},
		{"service that does not exist",
			"postgres://" + user + ":" + secret + "@localhost/hub?service=absent"},
		{"unknown channel_binding",
			"postgres://" + user + ":" + secret + "@localhost/hub?channel_binding=bogus"},
		{"invalid min_protocol_version",
			"postgres://" + user + ":" + secret + "@localhost/hub?min_protocol_version=bogus"},
		{"invalid max_protocol_version",
			"postgres://" + user + ":" + secret + "@localhost/hub?max_protocol_version=bogus"},
		{"host list the driver cannot split",
			"postgres://" + user + ":" + secret + "@a:b:5432/hub"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinPGEnv(t)
			t.Setenv("PGSERVICEFILE", filepath.Join(t.TempDir(), "absent.conf"))
			out, err := transport.EnforceTLSPosture(withSSLMode(tt.dsn, "disable"),
				transport.Options{InsecureTLS: true})
			require.Error(t, err)
			require.Empty(t, out)
			// The DSN is refused as unparsable, not as a host refusal: the
			// one parse failed, so no host was evaluated (MTIX-95.25).
			require.True(t, errors.Is(err, transport.ErrDSNMalformed), "want ErrDSNMalformed, got %v", err)
			require.False(t, errors.Is(err, transport.ErrTLSWeakNonLoopback), "a parse failure is not a host refusal")
			msg := err.Error()
			require.Equal(t, wantUnparsableMessage, msg)
			for _, forbidden := range []string{user, secret, "localhost", "db.example.com", "postgres://", "absent"} {
				require.NotContains(t, msg, forbidden, "message must name no host or credential")
			}
		})
	}
}

func TestEnforceTLSPosture_VerifyFullWithAnyHost_ReturnsDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		env  map[string]string
	}{
		{"host parameter", "postgres://u:pw@/hub?host=db.example.com", nil},
		{"PGHOST", "postgres://u:pw@/hub", map[string]string{"PGHOST": "db.example.com"}},
		{"authority list", "postgres://u:pw@localhost:5432,db.example.com:5432/hub", nil},
	}
	for _, tt := range tests {
		for _, opts := range []transport.Options{{}, {InsecureTLS: true}} {
			t.Run(fmt.Sprintf("%s/insecure=%t", tt.name, opts.InsecureTLS), func(t *testing.T) {
				pinPGEnv(t)
				for k, v := range tt.env {
					t.Setenv(k, v)
				}
				out, err := transport.EnforceTLSPosture(tt.dsn, opts)
				require.NoError(t, err)
				u, perr := url.Parse(out)
				require.NoError(t, perr)
				require.Equal(t, "verify-full", u.Query().Get("sslmode"))
			})
		}
	}
}
