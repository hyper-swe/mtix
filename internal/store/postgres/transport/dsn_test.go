// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// withMTIXDir creates a fresh .mtix directory under t.TempDir() and
// returns its path. Cleanup is automatic.
func withMTIXDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(d, 0o755))
	return d
}

func TestSource_RequiresMtixDir(t *testing.T) {
	_, err := transport.Source("")
	require.Error(t, err)
}

func TestSource_FromEnv(t *testing.T) {
	dir := withMTIXDir(t)
	t.Setenv(transport.EnvDSN, "postgres://user:pass@host/db")
	got, err := transport.Source(dir)
	require.NoError(t, err)
	require.Equal(t, "postgres://user:pass@host/db", got)
}

func TestSource_FromSecretsFile(t *testing.T) {
	dir := withMTIXDir(t)
	t.Setenv(transport.EnvDSN, "")
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, transport.SecretsFilename),
		[]byte("postgres://user:pass@host/db\n"),
		transport.SecretsRequiredMode,
	))
	got, err := transport.Source(dir)
	require.NoError(t, err)
	require.Equal(t, "postgres://user:pass@host/db", got, "trailing whitespace stripped")
}

func TestSource_RefusesEmptySecretsFile(t *testing.T) {
	dir := withMTIXDir(t)
	t.Setenv(transport.EnvDSN, "")
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, transport.SecretsFilename),
		[]byte("   \n"),
		transport.SecretsRequiredMode,
	))
	_, err := transport.Source(dir)
	require.Error(t, err)
	require.True(t, errors.Is(err, transport.ErrDSNNotConfigured))
}

func TestSource_RefusesLooseSecretsFileMode(t *testing.T) {
	dir := withMTIXDir(t)
	t.Setenv(transport.EnvDSN, "")
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, transport.SecretsFilename),
		[]byte("postgres://user:pass@host/db\n"),
		0o644,
	))
	_, err := transport.Source(dir)
	require.Error(t, err)
	require.True(t, errors.Is(err, transport.ErrSecretsFileMode))
}

func TestSource_NoSourceConfigured(t *testing.T) {
	dir := withMTIXDir(t)
	t.Setenv(transport.EnvDSN, "")
	_, err := transport.Source(dir)
	require.Error(t, err)
	require.True(t, errors.Is(err, transport.ErrDSNNotConfigured))
}

func TestSource_RefusesDSNInTrackedConfig(t *testing.T) {
	for _, ext := range []string{"yaml", "yml", "json"} {
		t.Run(ext, func(t *testing.T) {
			dir := withMTIXDir(t)
			t.Setenv(transport.EnvDSN, "postgres://user:pass@host/db") // even if env is set, fail closed
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, "config."+ext),
				[]byte("sync.dsn: postgres://leaked@host/db\n"),
				0o644,
			))
			_, err := transport.Source(dir)
			require.Error(t, err)
			require.True(t, errors.Is(err, transport.ErrDSNInTrackedFile))
		})
	}
}

func TestSource_TrackedConfigWithoutDSNKeyAccepted(t *testing.T) {
	dir := withMTIXDir(t)
	t.Setenv(transport.EnvDSN, "postgres://user:pass@host/db")
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("api.bind: 127.0.0.1\nlogging.level: info\n"),
		0o644,
	))
	got, err := transport.Source(dir)
	require.NoError(t, err, "tracked config without DSN keys is fine")
	require.Equal(t, "postgres://user:pass@host/db", got)
}

func TestEnforceTLS_DefaultsToVerifyFull(t *testing.T) {
	out, err := transport.EnforceTLSPosture(
		"postgres://user:pass@example.com/db",
		transport.Options{},
	)
	require.NoError(t, err)
	u, _ := url.Parse(out)
	require.Equal(t, "verify-full", u.Query().Get("sslmode"))
}

func TestEnforceTLS_PreservesExplicitVerifyFull(t *testing.T) {
	out, err := transport.EnforceTLSPosture(
		"postgres://user:pass@example.com/db?sslmode=verify-full",
		transport.Options{},
	)
	require.NoError(t, err)
	u, _ := url.Parse(out)
	require.Equal(t, "verify-full", u.Query().Get("sslmode"))
}

func TestEnforceTLS_RefusesWeakerWithoutInsecureFlag(t *testing.T) {
	cases := []string{"verify-ca", "prefer", "allow", "disable"}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			_, err := transport.EnforceTLSPosture(
				"postgres://user:pass@example.com/db?sslmode="+mode,
				transport.Options{InsecureTLS: false},
			)
			require.Error(t, err)
			require.True(t, errors.Is(err, transport.ErrTLSWeakWithoutFlag))
		})
	}
}

func TestEnforceTLS_AllowsWeakerWithInsecureFlagOnLoopback(t *testing.T) {
	cases := []string{"127.0.0.1", "localhost", "::1"}
	for _, host := range cases {
		t.Run(host, func(t *testing.T) {
			dsn := "postgres://user:pass@" + host + ":5432/db?sslmode=disable"
			out, err := transport.EnforceTLSPosture(dsn, transport.Options{InsecureTLS: true})
			require.NoError(t, err)
			require.Contains(t, out, "sslmode=disable")
		})
	}
}

func TestEnforceTLS_RefusesWeakerOnRemoteHostEvenWithInsecureFlag(t *testing.T) {
	_, err := transport.EnforceTLSPosture(
		"postgres://user:pass@db.example.com/db?sslmode=disable",
		transport.Options{InsecureTLS: true},
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, transport.ErrTLSWeakNonLoopback))
}

func TestEnforceTLS_HonorsSSLROOTCERTEnv(t *testing.T) {
	t.Setenv(transport.EnvSSLRootCert, "/path/to/ca.pem")
	out, err := transport.EnforceTLSPosture(
		"postgres://user:pass@example.com/db",
		transport.Options{},
	)
	require.NoError(t, err)
	u, _ := url.Parse(out)
	require.Equal(t, "/path/to/ca.pem", u.Query().Get("sslrootcert"))
}

func TestEnforceTLS_DoesNotOverrideExplicitSSLROOTCERT(t *testing.T) {
	t.Setenv(transport.EnvSSLRootCert, "/path/to/ca.pem")
	out, err := transport.EnforceTLSPosture(
		"postgres://user:pass@example.com/db?sslrootcert=/explicit.pem",
		transport.Options{},
	)
	require.NoError(t, err)
	u, _ := url.Parse(out)
	require.Equal(t, "/explicit.pem", u.Query().Get("sslrootcert"),
		"explicit DSN value beats env var")
}

func TestEnforceTLS_RejectsNonURLDSN(t *testing.T) {
	_, err := transport.EnforceTLSPosture("host=foo dbname=bar", transport.Options{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "postgres://")
}
