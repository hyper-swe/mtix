// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// MTIX-59: mtix sync backup shells out to pg_dump. Under a verifying sslmode
// with no trust root configured, libpq's pg_dump looks for
// ~/.postgresql/root.crt and aborts when it is absent. backupSSLRootCertEnv
// fills that gap ONLY when the operator has configured no trust root at all,
// defaulting to the OS trust store (system), as the sync transport does,
// without ever overriding a PGSSLROOTCERT env or an existing root.crt. A CA
// named in the DSN or MTIX_SYNC_SSLROOTCERT reaches pg_dump through the
// approved configuration instead (MTIX-95.7).

// isolateTrustEnv points HOME at an empty temp dir (no ~/.postgresql/root.crt)
// and clears PGSSLROOTCERT and MTIX_SYNC_SSLROOTCERT, so each case controls
// exactly one trust source.
func isolateTrustEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PGSSLROOTCERT", "")
	t.Setenv("MTIX_SYNC_SSLROOTCERT", "")
	return home
}

func TestBackupSSLRootCertEnv_SSLMode_SystemOnlyWhenVerifying(t *testing.T) {
	tests := []struct {
		sslmode string
		want    string
	}{
		{"verify-full", "system"},
		{"verify-ca", "system"},
		{"require", ""},
		{"prefer", ""},
		{"disable", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run("sslmode="+tt.sslmode, func(t *testing.T) {
			isolateTrustEnv(t)
			assert.Equal(t, tt.want, backupSSLRootCertEnv(tt.sslmode))
		})
	}
}

func TestBackupSSLRootCertEnv_PGSSLROOTCERTAlreadySet_NoOverride(t *testing.T) {
	isolateTrustEnv(t)
	t.Setenv("PGSSLROOTCERT", "/custom/ca.pem")
	assert.Empty(t, backupSSLRootCertEnv("verify-full"), "a preset PGSSLROOTCERT must be respected")
}

func TestBackupSSLRootCertEnv_DefaultRootCrtExists_NoOverride(t *testing.T) {
	home := isolateTrustEnv(t)
	pgDir := filepath.Join(home, ".postgresql")
	require.NoError(t, os.MkdirAll(pgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pgDir, "root.crt"), []byte("x"), 0o600))
	assert.Empty(t, backupSSLRootCertEnv("verify-full"),
		"an existing ~/.postgresql/root.crt is libpq's default trust root; do not override it")
}

func TestPgDumpConnParams_ExplicitSSLRootCert_NoSystemOverride(t *testing.T) {
	isolateTrustEnv(t)
	ca := writeBackupTestCA(t)
	t.Setenv("MTIX_SYNC_SSLROOTCERT", ca)
	c, err := pgDumpConnParams("postgres://u:p@host/db?sslmode=verify-full", transport.Options{})
	require.NoError(t, err)
	assert.Equal(t, ca, c.sslrootcert, "a named CA must be respected")
}
