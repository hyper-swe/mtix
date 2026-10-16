// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// These tests specify additive schema and creation identity independently of accessors.
func TestAttachSchema_FreshStore_TablesExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	db := newTestDB(t, path)
	for _, name := range []string{"agent_registrations", "wake_ledger", "wake_scan_cursor", "dispatcher_heartbeat", "board_flags", "attach_pending"} {
		t.Run(name, func(t *testing.T) {
			var count int
			require.NoError(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&count))
			assert.Equal(t, 1, count)
		})
	}
}

func TestAttachSchema_SchemaVersionUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	for range 2 {
		s, err := sqlite.New(path, slog.Default())
		require.NoError(t, err)
		require.NoError(t, s.Close())
	}
	var version string
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&version))
	assert.Equal(t, "4", version)
}

func TestStoreInstanceID_WrittenOnceAtCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	db := newTestDB(t, path)
	var first, second string
	require.NoError(t, db.QueryRow("SELECT value FROM meta WHERE key='store_instance_id'").Scan(&first))
	id, err := uuid.Parse(first)
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(4), id.Version())
	s, err = sqlite.New(path, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, db.QueryRow("SELECT value FROM meta WHERE key='store_instance_id'").Scan(&second))
	assert.Equal(t, first, second)
}

func TestStoreInstanceID_LegacyStore_RemainsAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db := newTestDB(t, path)
	_, err := db.Exec("CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO meta VALUES('schema_version','4')")
	require.NoError(t, err)
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM meta WHERE key='store_instance_id'").Scan(&count))
	assert.Zero(t, count)
}

func TestStoreInstanceID_LegacyMetadataWithoutVersion_RemainsAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db := newTestDB(t, path)
	_, err := db.Exec("CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO meta VALUES('custom','kept')")
	require.NoError(t, err)
	for range 2 {
		s, err := sqlite.New(path, slog.Default())
		require.NoError(t, err)
		require.NoError(t, s.Close())
	}
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM meta WHERE key='store_instance_id'").Scan(&count))
	assert.Zero(t, count)
}

func TestAttachSchema_LedgerKey_DistinguishesEpochAndRejectsDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	db := newTestDB(t, path)
	for _, epoch := range []int{1, 2} {
		_, err = db.Exec("INSERT INTO wake_ledger(agent_id,host_id,uid,event_seq,epoch,claimed_at,outcome) VALUES('worker','host','uid',1,?,'now','claimed')", epoch)
		require.NoError(t, err)
	}
	_, err = db.Exec("INSERT INTO wake_ledger(agent_id,host_id,uid,event_seq,epoch,claimed_at,outcome) VALUES('worker','host','uid',1,1,'later','error')")
	assert.Error(t, err)
	var nullable int
	require.NoError(t, db.QueryRow("SELECT [notnull] FROM pragma_table_info('agent_registrations') WHERE name='principal_id'").Scan(&nullable))
	assert.Zero(t, nullable)
	for _, index := range []string{"idx_agent_registrations_seen", "idx_wake_ledger_claims", "idx_attach_pending_queue"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?", index).Scan(&count))
		assert.Equal(t, 1, count)
	}
}

func TestStoreInstanceID_ExistingStoreWithoutMetadata_RemainsAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db := newTestDB(t, path)
	_, err := db.Exec("CREATE TABLE legacy_marker(value TEXT NOT NULL); INSERT INTO legacy_marker VALUES('kept')")
	require.NoError(t, err)
	for range 2 {
		s, err := sqlite.New(path, slog.Default())
		require.NoError(t, err)
		id, err := s.StoreInstanceID(t.Context())
		require.NoError(t, err)
		assert.Empty(t, id)
		require.NoError(t, s.Close())
	}
	var value string
	require.NoError(t, db.QueryRow("SELECT value FROM legacy_marker").Scan(&value))
	assert.Equal(t, "kept", value)
}

func TestStoreInstanceID_ExistingApplicationTable_RemainsAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db := newTestDB(t, path)
	_, err := db.Exec("CREATE TABLE sqliteXlegacy(value TEXT NOT NULL); INSERT INTO sqliteXlegacy VALUES('kept')")
	require.NoError(t, err)
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	id, err := s.StoreInstanceID(t.Context())
	require.NoError(t, err)
	assert.Empty(t, id)
	var value string
	require.NoError(t, db.QueryRow("SELECT value FROM sqliteXlegacy").Scan(&value))
	assert.Equal(t, "kept", value)
}
