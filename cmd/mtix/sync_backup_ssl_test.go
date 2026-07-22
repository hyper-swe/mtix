// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-59: mtix sync backup shells out to pg_dump. A verify-full/verify-ca hub
// DSN with no sslrootcert makes libpq's pg_dump look for ~/.postgresql/root.crt
// and abort when it is absent — a common failure for cloud hubs (Neon/Supabase
// DSNs are usually verify-full). backupSSLRootCertEnv fills that gap ONLY when
// the operator has configured no trust root at all, defaulting to the OS trust
// store (system) so public-CA providers back up out of the box, without ever
// overriding an sslrootcert, a PGSSLROOTCERT env, or an existing root.crt.

// isolateTrustEnv points HOME at an empty temp dir (no ~/.postgresql/root.crt)
// and clears PGSSLROOTCERT, so each case controls exactly one trust source.
func isolateTrustEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PGSSLROOTCERT", "")
	return home
}

func TestBackupSSLRootCertEnv_VerifyFullNoTrustRoot_DefaultsToSystem(t *testing.T) {
	isolateTrustEnv(t)
	assert.Equal(t, "system",
		backupSSLRootCertEnv("postgres://u:p@host/db?sslmode=verify-full"),
		"verify-full with no configured trust root must default to the system store")
}

func TestBackupSSLRootCertEnv_VerifyCaNoTrustRoot_DefaultsToSystem(t *testing.T) {
	isolateTrustEnv(t)
	assert.Equal(t, "system",
		backupSSLRootCertEnv("postgres://u:p@host/db?sslmode=verify-ca"))
}

func TestBackupSSLRootCertEnv_ExplicitSSLRootCert_NoOverride(t *testing.T) {
	isolateTrustEnv(t)
	assert.Empty(t,
		backupSSLRootCertEnv("postgres://u:p@host/db?sslmode=verify-full&sslrootcert=/etc/ca.pem"),
		"an explicit sslrootcert must be respected")
}

func TestBackupSSLRootCertEnv_NonVerifyingSSLMode_NoOverride(t *testing.T) {
	isolateTrustEnv(t)
	assert.Empty(t, backupSSLRootCertEnv("postgres://u:p@host/db?sslmode=require"))
	assert.Empty(t, backupSSLRootCertEnv("postgres://u:p@host/db?sslmode=disable"))
	assert.Empty(t, backupSSLRootCertEnv("postgres://u:p@host/db"),
		"no sslmode → no verification requested → no override")
}

func TestBackupSSLRootCertEnv_PGSSLROOTCERTAlreadySet_NoOverride(t *testing.T) {
	isolateTrustEnv(t)
	t.Setenv("PGSSLROOTCERT", "/custom/ca.pem")
	assert.Empty(t,
		backupSSLRootCertEnv("postgres://u:p@host/db?sslmode=verify-full"),
		"a preset PGSSLROOTCERT must be respected")
}

func TestBackupSSLRootCertEnv_DefaultRootCrtExists_NoOverride(t *testing.T) {
	home := isolateTrustEnv(t)
	pgDir := filepath.Join(home, ".postgresql")
	require.NoError(t, os.MkdirAll(pgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pgDir, "root.crt"), []byte("x"), 0o600))
	assert.Empty(t,
		backupSSLRootCertEnv("postgres://u:p@host/db?sslmode=verify-full"),
		"an existing ~/.postgresql/root.crt is libpq's default trust root; do not override it")
}
