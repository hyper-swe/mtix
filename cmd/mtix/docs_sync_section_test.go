// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// generateDocsIn runs `mtix docs generate --force` in a fresh project
// directory and returns the generated CLAUDE.md (MTIX-95.8.1).
func generateDocsIn(t *testing.T, writeSecrets bool) string {
	t.Helper()
	dir := t.TempDir()
	mtixDir := filepath.Join(dir, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))
	if writeSecrets {
		require.NoError(t, os.WriteFile(filepath.Join(mtixDir, transport.SecretsFilename),
			[]byte("postgresql://u:pw@db.example.test:5432/hub?sslmode=verify-full\n"), 0o600))
	}
	old, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(old) })
	require.NoError(t, runDocsGenerate(true))
	body, err := os.ReadFile(filepath.Join(mtixDir, "docs", "CLAUDE.md")) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	return string(body)
}

// TestRunDocsGenerate_HubDSNSource_SelectsSyncSection: the generated
// CLAUDE.md carries the full SYNC routine when MTIX_SYNC_DSN or
// .mtix/secrets configures a hub, and the one-line pointer otherwise
// (MTIX-95.8.1).
func TestRunDocsGenerate_HubDSNSource_SelectsSyncSection(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	require.Contains(t, generateDocsIn(t, false), "No sync hub is configured for this project")
	require.NotContains(t, generateDocsIn(t, false), "Session start: run `mtix sync push`")

	require.Contains(t, generateDocsIn(t, true), "Session start: run `mtix sync push`")

	t.Setenv(transport.EnvDSN, "postgresql://u:pw@db.example.test:5432/hub?sslmode=verify-full")
	full := generateDocsIn(t, false)
	require.Contains(t, full, "Session start: run `mtix sync push`")
	require.NotContains(t, full, "db.example.test", "the DSN is never written into the docs")
}
