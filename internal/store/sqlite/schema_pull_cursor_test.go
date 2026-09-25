// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// The event-id halves of the pull cursor and the clone checkpoint
// (MTIX-95.4; ADR-006 D6, D16): meta.sync.last_pulled_event_id and
// meta.sync.clone.checkpoint_event_id hold the event id of the last event
// pulled or cloned at the saved Lamport clock. They are seeded empty with
// INSERT OR IGNORE on every open, without a schema_version change, so an
// existing store gains them on its first open by this version and keeps its
// Lamport cursor.

// TestSchema_PullCursorEventIDKeysSeededEmpty: a fresh store has both keys,
// empty, next to their Lamport halves at 0.
func TestSchema_PullCursorEventIDKeysSeededEmpty(t *testing.T) {
	_, db := schemaTestEnv(t, filepath.Join(t.TempDir(), "fresh.db"))
	for _, c := range []struct{ key, want string }{
		{"meta.sync.last_pulled_clock", "0"},
		{"meta.sync.last_pulled_event_id", ""},
		{"meta.sync.clone.checkpoint", "0"},
		{"meta.sync.clone.checkpoint_event_id", ""},
	} {
		t.Run(c.key, func(t *testing.T) {
			v, ok := metaValue(t, db, c.key)
			require.Truef(t, ok, "%s must exist", c.key)
			require.Equal(t, c.want, v)
		})
	}
}

// TestSchema_StoreFromBeforeTupleCursor_GainsEmptyEventIDKeepsClock: a store
// written by a CLI older than MTIX-95.4 has a Lamport-only pull cursor and
// clone checkpoint. Reopened by this version it gains both event-id keys,
// empty, keeps both Lamport values, and keeps its schema_version: the empty
// event id makes its next pull read the events at exactly its Lamport clock
// again.
func TestSchema_StoreFromBeforeTupleCursor_GainsEmptyEventIDKeepsClock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.db")
	s, db := schemaTestEnv(t, dbPath)
	for _, stmt := range []string{
		`UPDATE meta SET value = '42' WHERE key = 'meta.sync.last_pulled_clock'`,
		`UPDATE meta SET value = '17' WHERE key = 'meta.sync.clone.checkpoint'`,
		`DELETE FROM meta WHERE key IN ('meta.sync.last_pulled_event_id', 'meta.sync.clone.checkpoint_event_id')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	require.NoError(t, s.Close())

	reopened, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })

	for _, c := range []struct{ key, want string }{
		{"meta.sync.last_pulled_clock", "42"},
		{"meta.sync.last_pulled_event_id", ""},
		{"meta.sync.clone.checkpoint", "17"},
		{"meta.sync.clone.checkpoint_event_id", ""},
		{"schema_version", "4"},
	} {
		v, ok := metaValue(t, db, c.key)
		require.Truef(t, ok, "%s must exist after the reopen", c.key)
		require.Equal(t, c.want, v, c.key)
	}
}
