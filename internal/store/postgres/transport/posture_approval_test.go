// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// hostEntry is one connection attempt in a parsed configuration: the
// host and port the driver dials and the TLS settings it uses there.
type hostEntry struct {
	Host               string
	Port               uint16
	TLS                bool
	ServerName         string
	InsecureSkipVerify bool
	RootCAs            *x509.CertPool
}

// entriesOf lists every connection attempt of c, the primary host first
// and then each fallback, in dial order.
func entriesOf(c *pgconn.Config) []hostEntry {
	all := append([]*pgconn.FallbackConfig{{Host: c.Host, Port: c.Port, TLSConfig: c.TLSConfig}}, c.Fallbacks...)
	out := make([]hostEntry, 0, len(all))
	for _, f := range all {
		e := hostEntry{Host: f.Host, Port: f.Port, TLS: f.TLSConfig != nil}
		if f.TLSConfig != nil {
			e.ServerName = f.TLSConfig.ServerName
			e.InsecureSkipVerify = f.TLSConfig.InsecureSkipVerify
			e.RootCAs = f.TLSConfig.RootCAs
		}
		out = append(out, e)
	}
	return out
}

// requireSameEntries fails unless got lists the same hosts, ports and
// TLS settings as want, in the same order. Root CA pools compare by
// content.
func requireSameEntries(t *testing.T, want, got []hostEntry) {
	t.Helper()
	require.Len(t, got, len(want), "number of connection attempts")
	for i := range want {
		w, g := want[i], got[i]
		require.Equal(t, w.Host, g.Host, "entry %d host", i)
		require.Equal(t, w.Port, g.Port, "entry %d port", i)
		require.Equal(t, w.TLS, g.TLS, "entry %d TLS on/off", i)
		require.Equal(t, w.ServerName, g.ServerName, "entry %d ServerName", i)
		require.Equal(t, w.InsecureSkipVerify, g.InsecureSkipVerify, "entry %d InsecureSkipVerify", i)
		require.True(t, w.RootCAs.Equal(g.RootCAs), "entry %d root CAs", i)
	}
}

// hostList returns "host:port" for each host of entries, counting
// consecutive attempts at the same host and port once (the driver tries
// a host twice under allow and prefer).
func hostList(entries []hostEntry) []string {
	var out []string
	for i, e := range entries {
		if i > 0 && e.Host == entries[i-1].Host && e.Port == entries[i-1].Port {
			continue
		}
		out = append(out, net.JoinHostPort(e.Host, strconv.Itoa(int(e.Port))))
	}
	return out
}

// isSocketHost reports whether host is a Unix-domain socket directory.
func isSocketHost(host string) bool { return strings.HasPrefix(host, "/") }

// isLoopbackHost reports whether host is "localhost" or a loopback IP.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// postureForm is one DSN form the posture step approves. "{ca}" in the
// DSN or an env value is replaced by the path of a test CA file; a
// serviceHost is written to a service file as in hostCase.
type postureForm struct {
	hostCase
	mode      string
	wantHosts []string
	wantCA    bool
}

// verifyFullForms lists the verify-full forms: host lists, fallbacks,
// PG* environment, service file and every source of a CA file.
func verifyFullForms() []postureForm {
	one := []string{"db.example.com:5432"}
	two := []string{"db1.example.com:5433", "db2.example.com:5434"}
	return []postureForm{
		{hostCase: hostCase{name: "remote authority host", dsn: "postgres://u:pw@db.example.com/hub"}, wantHosts: one},
		{hostCase: hostCase{name: "explicit verify-full", dsn: "postgres://u:pw@db.example.com/hub?sslmode=verify-full"}, wantHosts: one},
		{hostCase: hostCase{name: "authority host list",
			dsn: "postgres://u:pw@db1.example.com:5433,db2.example.com:5434/hub"}, wantHosts: two},
		{hostCase: hostCase{name: "host parameter list",
			dsn: "postgres://u:pw@/hub?host=db1.example.com,db2.example.com&port=5433,5434"}, wantHosts: two},
		{hostCase: hostCase{name: "PGHOST and PGPORT lists", dsn: "postgres://u:pw@/hub",
			env: map[string]string{"PGHOST": "db1.example.com,db2.example.com", "PGPORT": "5433,5434"}}, wantHosts: two},
		{hostCase: hostCase{name: "service file host list", dsn: "postgres://u:pw@/hub?service=hub",
			serviceHost: "db1.example.com,db2.example.com"},
			wantHosts: []string{"db1.example.com:5432", "db2.example.com:5432"}},
		{hostCase: hostCase{name: "address hosts", dsn: "postgres://u:pw@/hub?host=10.0.0.5,2001:db8::1&port=5432,5433"},
			wantHosts: []string{"10.0.0.5:5432", "[2001:db8::1]:5433"}},
		{hostCase: hostCase{name: "socket and remote host", dsn: "postgres://u:pw@/hub?host=/var/run/postgresql,db.example.com"},
			wantHosts: []string{"/var/run/postgresql:5432", "db.example.com:5432"}},
		{hostCase: hostCase{name: "CA from MTIX_SYNC_SSLROOTCERT", dsn: "postgres://u:pw@db.example.com/hub",
			env: map[string]string{transport.EnvSSLRootCert: "{ca}"}}, wantHosts: one, wantCA: true},
		{hostCase: hostCase{name: "CA named in the DSN", dsn: "postgres://u:pw@db.example.com/hub?sslrootcert={ca}"},
			wantHosts: one, wantCA: true},
		{hostCase: hostCase{name: "CA from PGSSLROOTCERT", dsn: "postgres://u:pw@db.example.com/hub",
			env: map[string]string{"PGSSLROOTCERT": "{ca}"}}, wantHosts: one, wantCA: true},
	}
}

// weakForms lists local forms under every weak sslmode: loopback host
// lists, a socket from PGHOST, a service file and a CA from
// MTIX_SYNC_SSLROOTCERT.
func weakForms() []postureForm {
	var out []postureForm
	for _, mode := range weakModes {
		out = append(out,
			postureForm{hostCase: hostCase{name: "loopback authority list",
				dsn: "postgres://u:pw@localhost:5433,127.0.0.1:5434/hub"}, mode: mode,
				wantHosts: []string{"localhost:5433", "127.0.0.1:5434"}},
			postureForm{hostCase: hostCase{name: "PGHOST list of loopback address and socket",
				dsn: "postgres://u:pw@/hub", env: map[string]string{"PGHOST": "::1,/tmp"}}, mode: mode,
				wantHosts: []string{"[::1]:5432", "/tmp:5432"}},
			postureForm{hostCase: hostCase{name: "loopback service host", dsn: "postgres://u:pw@/hub?service=hub",
				serviceHost: "localhost"}, mode: mode, wantHosts: []string{"localhost:5432"}},
			postureForm{hostCase: hostCase{name: "loopback host with CA from MTIX_SYNC_SSLROOTCERT",
				dsn: "postgres://u:pw@127.0.0.1/hub", env: map[string]string{transport.EnvSSLRootCert: "{ca}"}},
				mode: mode, wantHosts: []string{"127.0.0.1:5432"}, wantCA: true},
		)
	}
	return out
}

// noDefaultCA is an explicit empty sslrootcert. DSN settings override
// the driver's defaults, so it keeps the driver's default CA file
// (~/.postgresql/root.crt, loaded when it exists) out of the no-CA cases
// on any machine. pinPGEnv cannot do this: an empty PGSSLROOTCERT is
// ignored, and the default path comes from the OS account, not $HOME.
const noDefaultCA = "sslrootcert="

// withParam appends the query parameter kv to dsn.
func withParam(dsn, kv string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&" + kv
	}
	return dsn + "?" + kv
}

// applyPostureForm pins the environment for f, substitutes caPath for
// "{ca}" and returns the DSN and the options to approve it with. A form
// that expects no CA gets noDefaultCA.
func applyPostureForm(t *testing.T, f postureForm, caPath string) (string, transport.Options) {
	t.Helper()
	tc := f.hostCase
	env := make(map[string]string, len(tc.env))
	for k, v := range tc.env {
		env[k] = strings.ReplaceAll(v, "{ca}", caPath)
	}
	tc.env = env
	dsn := strings.ReplaceAll(applyHostCase(t, tc), "{ca}", url.QueryEscape(caPath))
	if !f.wantCA {
		dsn = withParam(dsn, noDefaultCA)
	}
	if f.mode == "" {
		return dsn, transport.Options{}
	}
	return withSSLMode(dsn, f.mode), transport.Options{InsecureTLS: true}
}

// formName names a subtest after the form and its sslmode.
func formName(f postureForm) string {
	if f.mode == "" {
		return f.name + "/verify-full"
	}
	return f.name + "/" + f.mode
}

// requireRootCAs fails unless every TLS entry carries want (nil: none).
func requireRootCAs(t *testing.T, entries []hostEntry, want *x509.CertPool) {
	t.Helper()
	for i, e := range entries {
		if e.TLS {
			require.True(t, want.Equal(e.RootCAs), "entry %d root CAs", i)
		}
	}
}

func TestApproveDSN_VerifyFull_EveryNetworkHostVerifiesCertificates(t *testing.T) {
	caPath, caPool := writeTestCA(t)
	for _, f := range verifyFullForms() {
		t.Run(formName(f), func(t *testing.T) {
			dsn, opts := applyPostureForm(t, f, caPath)
			a, err := transport.ApproveDSN(dsn, opts)
			require.NoError(t, err)
			require.NotNil(t, a.Config)
			require.Equal(t, "verify-full", a.SSLMode)
			entries := entriesOf(&a.Config.ConnConfig.Config)
			require.Equal(t, f.wantHosts, hostList(entries))
			for i, e := range entries {
				if isSocketHost(e.Host) {
					require.False(t, e.TLS, "entry %d: the driver never wraps a local socket in TLS", i)
					continue
				}
				require.True(t, e.TLS, "entry %d uses TLS", i)
				require.False(t, e.InsecureSkipVerify, "entry %d verifies the certificate", i)
				require.Equal(t, e.Host, e.ServerName, "entry %d verifies the host name", i)
			}
			wantPool := (*x509.CertPool)(nil)
			if f.wantCA {
				wantPool = caPool
			}
			requireRootCAs(t, entries, wantPool)
			require.Equal(t, f.wantCA, a.CASupplied)
			u, perr := url.Parse(a.DSNForTest())
			require.NoError(t, perr)
			require.Equal(t, "verify-full", u.Query().Get("sslmode"))
		})
	}
}

func TestApproveDSN_WeakMode_EveryHostIsLoopbackOrLocalSocket(t *testing.T) {
	caPath, caPool := writeTestCA(t)
	for _, f := range weakForms() {
		t.Run(formName(f), func(t *testing.T) {
			dsn, opts := applyPostureForm(t, f, caPath)
			a, err := transport.ApproveDSN(dsn, opts)
			require.NoError(t, err)
			require.Equal(t, f.mode, a.SSLMode)
			entries := entriesOf(&a.Config.ConnConfig.Config)
			require.Equal(t, f.wantHosts, hostList(entries))
			for i, e := range entries {
				require.True(t, isSocketHost(e.Host) || isLoopbackHost(e.Host),
					"entry %d is neither loopback nor a local socket", i)
			}
			wantPool := (*x509.CertPool)(nil)
			if f.wantCA {
				wantPool = caPool
			}
			requireRootCAs(t, entries, wantPool)
			u, perr := url.Parse(a.DSNForTest())
			require.NoError(t, perr)
			require.Equal(t, f.mode, u.Query().Get("sslmode"), "caller's sslmode is kept")
		})
	}
}

func TestApproveDSN_CASupplied_ReportsWhetherTLSCarriesRootCA(t *testing.T) {
	caPath, _ := writeTestCA(t)
	tests := []struct {
		name string
		dsn  string
		env  map[string]string
		opts transport.Options
		want bool
	}{
		{"no CA", "postgres://u:pw@db.example.com/hub?" + noDefaultCA, nil, transport.Options{}, false},
		{"explicit empty sslrootcert overrides PGSSLROOTCERT", "postgres://u:pw@db.example.com/hub?" + noDefaultCA,
			map[string]string{"PGSSLROOTCERT": caPath}, transport.Options{}, false},
		{"CA named in the DSN", "postgres://u:pw@db.example.com/hub?sslrootcert=" + url.QueryEscape(caPath),
			nil, transport.Options{}, true},
		{"CA from MTIX_SYNC_SSLROOTCERT", "postgres://u:pw@db.example.com/hub",
			map[string]string{transport.EnvSSLRootCert: caPath}, transport.Options{}, true},
		{"CA from PGSSLROOTCERT", "postgres://u:pw@db.example.com/hub",
			map[string]string{"PGSSLROOTCERT": caPath}, transport.Options{}, true},
		{"CA with prefer on loopback", "postgres://u:pw@localhost/hub?sslmode=prefer",
			map[string]string{transport.EnvSSLRootCert: caPath}, transport.Options{InsecureTLS: true}, true},
		{"CA with TLS disabled on loopback", "postgres://u:pw@localhost/hub?sslmode=disable",
			map[string]string{transport.EnvSSLRootCert: caPath}, transport.Options{InsecureTLS: true}, false},
		{"socket host only", "postgres://u:pw@/hub?host=/tmp", map[string]string{transport.EnvSSLRootCert: caPath},
			transport.Options{}, false},
		{"CA with allow on loopback: the first entry has no TLS", "postgres://u:pw@localhost/hub?sslmode=allow",
			map[string]string{transport.EnvSSLRootCert: caPath}, transport.Options{InsecureTLS: true}, true},
		{"socket primary host with a remote fallback and a CA", "postgres://u:pw@/hub?host=/tmp,db.example.com",
			map[string]string{transport.EnvSSLRootCert: caPath}, transport.Options{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinPGEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			a, err := transport.ApproveDSN(tt.dsn, tt.opts)
			require.NoError(t, err)
			require.Equal(t, tt.want, a.CASupplied)
		})
	}
}

// wantUnparsableMessage is the one fixed message for connection settings
// the driver cannot parse, a certificate file it cannot load included,
// under every sslmode (FR-18.17, MTIX-95.25).
const wantUnparsableMessage = "pgxpool parse: DSN could not be parsed: check the DSN's connection parameters"

func TestApproveDSN_CertificateFileUnusable_ReturnsFixedMessage(t *testing.T) {
	const user, secret, host = "certuser", "Wq7nR2xZ5kLp", "db.example.com"
	base := "postgres://" + user + ":" + secret + "@" + host + "/hub"
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent-file.pem")
	notPEM := filepath.Join(dir, "garbled-file.pem")
	require.NoError(t, os.WriteFile(notPEM, []byte("no certificate here\n"), 0o600))
	clientCert, _ := writeTestClientCert(t)
	tests := []struct {
		name string
		dsn  string
		env  map[string]string
	}{
		{"sslrootcert in the DSN names a missing file", base + "?sslrootcert=" + url.QueryEscape(missing), nil},
		{"MTIX_SYNC_SSLROOTCERT names a missing file", base, map[string]string{transport.EnvSSLRootCert: missing}},
		{"PGSSLROOTCERT names a missing file", base, map[string]string{"PGSSLROOTCERT": missing}},
		{"sslrootcert names a file without certificates", base + "?sslrootcert=" + url.QueryEscape(notPEM), nil},
		{"client certificate without its key", base, map[string]string{"PGSSLCERT": clientCert}},
		{"client key file missing", base, map[string]string{"PGSSLCERT": clientCert, "PGSSLKEY": missing}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinPGEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			a, err := transport.ApproveDSN(tt.dsn, transport.Options{})
			require.Error(t, err)
			require.Nil(t, a)
			require.Equal(t, wantUnparsableMessage, err.Error())
			require.ErrorIs(t, err, transport.ErrDSNMalformed)
			for _, forbidden := range []string{user, secret, host, dir, "absent-file", "garbled-file", "client.pem"} {
				require.NotContains(t, err.Error(), forbidden, "message must name no setting value")
			}
			// The check refuses before any connection; the timeout only
			// bounds a regression that would dial.
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			pool, perr := transport.New(ctx, tt.dsn, transport.Options{})
			require.Nil(t, pool)
			require.Error(t, perr)
			require.Equal(t, "tls posture: "+wantUnparsableMessage, perr.Error())
		})
	}
}

func TestApproveDSN_CertificateFileUnusableUnderWeakMode_ReturnsFixedParseMessage(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent-file.pem")
	notPEM := filepath.Join(dir, "garbled-file.pem")
	require.NoError(t, os.WriteFile(notPEM, []byte("no certificate here\n"), 0o600))
	tests := []struct {
		name string
		dsn  string
		env  map[string]string
	}{
		{"MTIX_SYNC_SSLROOTCERT names a missing file under require", "postgres://u:pw@localhost/hub?sslmode=require",
			map[string]string{transport.EnvSSLRootCert: missing}},
		{"stale PGSSLROOTCERT under disable", "postgres://u:pw@127.0.0.1/hub?sslmode=disable",
			map[string]string{"PGSSLROOTCERT": missing}},
		{"sslrootcert without certificates under prefer", "postgres://u:pw@/hub?host=/tmp,::1&sslmode=prefer&sslrootcert=" +
			url.QueryEscape(notPEM), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinPGEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			a, err := transport.ApproveDSN(tt.dsn, transport.Options{InsecureTLS: true})
			require.Error(t, err)
			require.Nil(t, a)
			require.Equal(t, wantUnparsableMessage, err.Error(), "a parse failure has one fixed message")
			require.ErrorIs(t, err, transport.ErrDSNMalformed)
			require.NotErrorIs(t, err, transport.ErrTLSWeakNonLoopback, "a file that cannot be loaded is not a host refusal")
			for _, forbidden := range []string{"absent-file", "garbled-file", dir, "localhost", "127.0.0.1", "loopback"} {
				require.NotContains(t, err.Error(), forbidden)
			}
		})
	}
}

func TestApproveDSN_Refusal_ReturnsNoApprovalAndTheEnforceTLSPostureError(t *testing.T) {
	const user, secret = "refuseuser", "Hj4tY8pQ2vNc"
	creds := user + ":" + secret
	tests := []struct {
		name string
		dsn  string
		opts transport.Options
		want error
	}{
		{"weak mode without the flag", "postgres://" + creds + "@db.example.com/hub?sslmode=disable",
			transport.Options{}, transport.ErrTLSWeakWithoutFlag},
		{"weak mode on a remote fallback", "postgres://" + creds + "@localhost,db.example.com/hub?sslmode=disable",
			transport.Options{InsecureTLS: true}, transport.ErrTLSWeakNonLoopback},
		{"weak mode on a remote fallback under prefer", "postgres://" + creds + "@localhost,db.example.com/hub?sslmode=prefer",
			transport.Options{InsecureTLS: true}, transport.ErrTLSWeakNonLoopback},
		{"not a postgres URL", "mysql://" + creds + "@db.example.com/hub", transport.Options{}, transport.ErrDSNMalformed},
		{"driver cannot parse under verify-full", "postgres://" + creds + "@db.example.com/hub?connect_timeout=bogus",
			transport.Options{}, transport.ErrDSNMalformed},
		{"driver cannot parse under a weak mode", "postgres://" + creds + "@localhost/hub?sslmode=disable&connect_timeout=bogus",
			transport.Options{InsecureTLS: true}, transport.ErrDSNMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinPGEnv(t)
			a, err := transport.ApproveDSN(tt.dsn, tt.opts)
			require.Error(t, err)
			require.Nil(t, a)
			require.True(t, errors.Is(err, tt.want), "want %v, got %v", tt.want, err)
			for _, forbidden := range []string{user, secret, "db.example.com", "localhost"} {
				require.NotContains(t, err.Error(), forbidden, "message must name no setting value")
			}
			out, eerr := transport.EnforceTLSPosture(tt.dsn, tt.opts)
			require.Empty(t, out)
			require.Error(t, eerr)
			require.Equal(t, err.Error(), eerr.Error())
		})
	}
}

func TestApproveDSN_WeakModeRemoteFallback_MessageCountsEachHostOnce(t *testing.T) {
	for _, mode := range weakModes {
		t.Run(mode, func(t *testing.T) {
			pinPGEnv(t)
			_, err := transport.ApproveDSN("postgres://u:pw@localhost,db.example.com/hub?sslmode="+mode,
				transport.Options{InsecureTLS: true})
			require.Error(t, err)
			require.ErrorIs(t, err, transport.ErrTLSWeakNonLoopback)
			require.Contains(t, err.Error(), "host 2 of 2 ")
		})
	}
}

func TestApproveDSN_UnrecognizedSSLMode_MessageOmitsValue(t *testing.T) {
	const value = "Zt6mB3qR9wXe"
	tests := []struct {
		name    string
		host    string
		opts    transport.Options
		want    error
		wantMsg string
	}{
		{"without the flag", "db.example.com", transport.Options{}, transport.ErrTLSWeakWithoutFlag,
			"sslmode=(unrecognized): weak sslmode requires --insecure-tls"},
		{"with the flag on loopback", "localhost", transport.Options{InsecureTLS: true}, transport.ErrDSNMalformed,
			wantUnparsableMessage},
		{"with the flag on a remote host", "db.example.com", transport.Options{InsecureTLS: true}, transport.ErrDSNMalformed,
			wantUnparsableMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinPGEnv(t)
			a, err := transport.ApproveDSN("postgres://u:pw@"+tt.host+"/hub?sslmode="+value, tt.opts)
			require.Error(t, err)
			require.Nil(t, a)
			require.ErrorIs(t, err, tt.want)
			require.Equal(t, tt.wantMsg, err.Error())
			for _, w := range secretWindows(strings.ToLower(value)) {
				require.NotContainsf(t, strings.ToLower(err.Error()), w, "message repeats sslmode fragment %q", w)
			}
		})
	}
}

func TestApproval_Printed_ShowsOnlyValueFreeSummary(t *testing.T) {
	const user, secret, host = "printuser", "Mx5vT8kQ3nWz", "db1.example.com"
	pinPGEnv(t)
	caPath, _ := writeTestCA(t)
	a, err := transport.ApproveDSN("postgres://"+user+":"+secret+"@"+host+",db2.example.com/hub?sslrootcert="+
		url.QueryEscape(caPath), transport.Options{})
	require.NoError(t, err)
	const want = "transport.Approval{sslmode=verify-full hosts=2 ca_supplied=true}"
	verbs := []string{"%v", "%+v", "%s", "%#v", "%q", "%d", "%+d", "%t", "%x", "%X", "%o", "%e", "%g", "%c", "%U",
		"%b", "%10v", "%-40s", "% x", "%#x", "%08d"}
	for _, verb := range verbs {
		for _, v := range []any{a, *a} {
			got := fmt.Sprintf(verb, v)
			require.Equal(t, want, got, "verb %s", verb)
			for _, forbidden := range []string{user, secret, host, caPath, "postgres://"} {
				require.NotContains(t, got, forbidden, "verb %s", verb)
			}
		}
	}
	require.Equal(t, want, fmt.Sprint(a))
	require.Equal(t, want, fmt.Sprintln(*a)[:len(want)])
	require.Equal(t, "transport.Approval{sslmode=(unrecognized) hosts=0 ca_supplied=false}",
		transport.Approval{SSLMode: secret}.String())

	// Under prefer the driver tries each network host twice (with and
	// without TLS); the summary counts hosts, not connection attempts.
	prefer, err := transport.ApproveDSN("postgres://u:pw@/hub?host=localhost,127.0.0.1,/tmp&sslmode=prefer&"+noDefaultCA,
		transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	require.Len(t, entriesOf(&prefer.Config.ConnConfig.Config), 5, "two attempts per network host, one for the socket")
	require.Equal(t, "transport.Approval{sslmode=prefer hosts=3 ca_supplied=false}", prefer.String())
}

func TestEnforceTLSPosture_ApprovedDSN_ReturnsApprovalDSN(t *testing.T) {
	caPath, _ := writeTestCA(t)
	forms := append(verifyFullForms(), weakForms()...)
	for _, f := range forms {
		t.Run(formName(f), func(t *testing.T) {
			dsn, opts := applyPostureForm(t, f, caPath)
			a, err := transport.ApproveDSN(dsn, opts)
			require.NoError(t, err)
			out, err := transport.EnforceTLSPosture(dsn, opts)
			require.NoError(t, err)
			require.Equal(t, a.DSNForTest(), out)
		})
	}
}
