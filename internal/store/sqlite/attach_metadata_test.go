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

// Metadata accessors preserve stored identities and reject malformed protocol values.
func TestStoreInstanceID_CreatedStore_ReturnsStoredValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	var stored string
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT value FROM meta WHERE key='store_instance_id'").Scan(&stored))
	got, err := s.StoreInstanceID(t.Context())
	require.NoError(t, err)
	assert.Equal(t, stored, got)
}
func TestMinDispatchProtocol_SetValue_PersistsAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.SetMinDispatchProtocol(t.Context(), 3))
	require.NoError(t, s.Close())
	s, err = sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	got, err := s.MinDispatchProtocol(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 3, got)
	assert.Error(t, s.SetMinDispatchProtocol(t.Context(), -1))
}
func TestMinDispatchProtocol_InvalidStoredValue_ReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	db := newTestDB(t, path)
	for _, value := range []string{"invalid", "-1", "999999999999999999999999999999999"} {
		_, err = db.Exec("INSERT OR REPLACE INTO meta(key,value) VALUES('min_dispatch_protocol',?)", value)
		require.NoError(t, err)
		_, err = s.MinDispatchProtocol(t.Context())
		assert.Error(t, err)
	}
}
