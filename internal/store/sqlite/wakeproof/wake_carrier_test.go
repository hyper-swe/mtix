// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package wakeproof_test

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const legacyWakeSchema = `CREATE TABLE wake_ledger (
 agent_id TEXT NOT NULL, host_id TEXT NOT NULL, uid TEXT NOT NULL,
 event_seq INTEGER NOT NULL, epoch INTEGER NOT NULL,
 claimed_at TEXT NOT NULL, outcome TEXT NOT NULL,
 PRIMARY KEY(agent_id,host_id,uid,event_seq,epoch))`

func TestWakeFenceCarrier_StoreOpen_PreservesRowAndDurableFence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	seedLegacyWakeCarrier(t, path)
	store := openWakeCarrierStore(t, path)
	want := readOriginalWakeCarrier(t, store)
	fence := "00112233445566778899aabbccddeeff"
	_, err := store.WriteDB().ExecContext(t.Context(),
		"UPDATE wake_ledger SET attempt_token=? WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=?",
		fence, "worker", "host", "uid", 7, 2)
	require.NoError(t, err, "Store-open must expose the durable wake-attempt fence carrier")
	require.Equal(t, want, readOriginalWakeCarrier(t, store))
	require.NoError(t, store.Close())
	reopened := openWakeCarrierStore(t, path)
	var got, version string
	require.NoError(t, reopened.ReadDB().QueryRowContext(t.Context(),
		"SELECT attempt_token FROM wake_ledger WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=?",
		"worker", "host", "uid", 7, 2).Scan(&got))
	require.Equal(t, fence, got)
	require.Equal(t, want, readOriginalWakeCarrier(t, reopened))
	require.NoError(t, reopened.ReadDB().QueryRowContext(t.Context(),
		"SELECT value FROM meta WHERE key=?", "schema_version").Scan(&version))
	require.Equal(t, "4", version)
}

func seedLegacyWakeCarrier(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), legacyWakeSchema)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(),
		"INSERT INTO wake_ledger(agent_id,host_id,uid,event_seq,epoch,claimed_at,outcome) VALUES(?,?,?,?,?,?,?)",
		"worker", "host", "uid", 7, 2, "2026-10-14T00:00:00Z", "claimed")
	require.NoError(t, err)
}

func readOriginalWakeCarrier(t *testing.T, store *sqlite.Store) []any {
	t.Helper()
	var agent, host, uid, claimed, outcome string
	var seq, epoch int64
	require.NoError(t, store.ReadDB().QueryRowContext(t.Context(),
		"SELECT agent_id,host_id,uid,event_seq,epoch,claimed_at,outcome FROM wake_ledger WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=?",
		"worker", "host", "uid", 7, 2).Scan(&agent, &host, &uid, &seq, &epoch, &claimed, &outcome))
	return []any{agent, host, uid, seq, epoch, claimed, outcome}
}

func TestWakeFenceCarrier_StoreOpen_OriginalRowControl(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	seedLegacyWakeCarrier(t, path)
	store := openWakeCarrierStore(t, path)
	require.Equal(t, []any{"worker", "host", "uid", int64(7), int64(2),
		"2026-10-14T00:00:00Z", "claimed"}, readOriginalWakeCarrier(t, store))
}

func openWakeCarrierStore(t *testing.T, path string) *sqlite.Store {
	t.Helper()
	store, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}
