// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Default metadata and malformed present values have distinct contracts.
func TestMinDispatchProtocol_FreshStore_PersistsZeroDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	var value string
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT value FROM meta WHERE key='min_dispatch_protocol'").Scan(&value))
	assert.Equal(t, "0", value)
}
func TestMinDispatchProtocol_EmptyStoredValue_ReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	_, err = newTestDB(t, path).Exec("INSERT OR REPLACE INTO meta(key,value) VALUES('min_dispatch_protocol','')")
	require.NoError(t, err)
	_, err = s.MinDispatchProtocol(t.Context())
	assert.Error(t, err)
}
func TestMinDispatchProtocol_LegacyStoredValue_PreservesFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db := newTestDB(t, path)
	_, err := db.Exec("CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO meta VALUES('min_dispatch_protocol','8')")
	require.NoError(t, err)
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	got, err := s.MinDispatchProtocol(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 8, got)
}
