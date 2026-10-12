// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// These tests verify the additive board-local wake lease schema.

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestSchema_WakeLeasesAdditive_CreatesKeyedLocalTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	db := newTestDB(t, path)
	var definition, version string
	err = db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='wake_leases'").Scan(&definition)
	require.NoError(t, err, "wake lease table must exist")
	for _, column := range []string{"agent_id", "host_id", "uid", "holder", "high_water", "acquired_at", "expires_at", "dirty"} {
		assert.Contains(t, definition, column)
	}
	require.NoError(t, db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&version))
	assert.Equal(t, "4", version)
	_, err = db.Exec("INSERT INTO wake_leases VALUES (?,?,?,?,?,?,?,?)", "TEST-agent", "TEST-host", "1000", "0123456789abcdef0123456789abcdef", 0, "2026-10-11T00:00:00Z", "2026-10-11T00:15:00Z", 2)
	assert.Error(t, err, "dirty is a boolean constraint")
}
