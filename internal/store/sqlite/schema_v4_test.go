// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// TestSchema_FreshDBIsV4 asserts the schema version bump to v4
// (MTIX-30.6 / ADR-003 §7 Phase 3): sync_events grows a uid column.
func TestSchema_FreshDBIsV4(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	_, db := schemaTestEnv(t, dbPath)

	v, ok := metaValue(t, db, "schema_version")
	require.True(t, ok)
	require.Equal(t, "4", v, "fresh DB must report v4 after the MTIX-30.6 sync_events.uid add")
}

// TestSchema_FreshDB_SyncEventsHasUID asserts the new uid column exists
// on sync_events (ADR-003 §3: events reference the node by uid).
func TestSchema_FreshDB_SyncEventsHasUID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	_, db := schemaTestEnv(t, dbPath)

	cols := columnsOf(t, db, "sync_events")
	require.Contains(t, cols, "uid",
		"sync_events must carry uid for the dual-carry transition (ADR-003 §7 Phase 3)")
}

// TestSchema_V3ToV4Migration simulates a v3 database (nodes.uid present,
// sync_events WITHOUT uid) and asserts the v3->v4 ALTER runs cleanly,
// adds sync_events.uid, preserves existing rows, and bumps the version.
func TestSchema_V3ToV4Migration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v3.db")

	// Build a v3-shape DB: open with the current store (gives us a full
	// schema), then forcibly downgrade sync_events to the v3 shape (drop
	// the uid column) and set schema_version='3'.
	{
		s, err := sqlite.New(dbPath, slog.Default())
		require.NoError(t, err)
		require.NoError(t, s.Close())

		raw, err := sql.Open("sqlite", dbPath)
		require.NoError(t, err)
		ctx := context.Background()
		// Seed a sync_events row using only v3 columns.
		_, err = raw.ExecContext(ctx, `
			INSERT INTO nodes (id, parent_id, depth, seq, project, title,
			                   status, progress, created_at, updated_at, uid)
			VALUES ('MTIX-1', NULL, 0, 1, 'MTIX', 't', 'open', 0,
			        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'uid-1')`)
		require.NoError(t, err)
		// Rebuild sync_events without uid to emulate a true v3 table, then
		// re-stamp the version to 3 so init runs the v3->v4 step.
		_, err = raw.ExecContext(ctx, `
			DROP TABLE sync_events;
			CREATE TABLE sync_events (
			    event_id            TEXT PRIMARY KEY,
			    project_prefix      TEXT NOT NULL,
			    node_id             TEXT NOT NULL,
			    op_type             TEXT NOT NULL,
			    payload             TEXT NOT NULL,
			    wall_clock_ts       INTEGER NOT NULL,
			    lamport_clock       INTEGER NOT NULL,
			    vector_clock        TEXT NOT NULL,
			    author_id           TEXT NOT NULL,
			    author_machine_hash TEXT NOT NULL,
			    sync_status         TEXT NOT NULL DEFAULT 'pending',
			    created_at          TEXT NOT NULL,
			    retained_until      TEXT
			);
			INSERT INTO sync_events
			  (event_id, project_prefix, node_id, op_type, payload,
			   wall_clock_ts, lamport_clock, vector_clock,
			   author_id, author_machine_hash, sync_status, created_at)
			VALUES ('evt-1', 'MTIX', 'MTIX-1', 'create_node', 'null',
			        1, 1, '{}', 'alice', '0123456789abcdef',
			        'applied', '2026-01-01T00:00:00Z');
			UPDATE meta SET value = '3' WHERE key = 'schema_version';`)
		require.NoError(t, err)
		require.NoError(t, raw.Close())
	}

	// Run the migration via production init.
	_, db := schemaTestEnv(t, dbPath)

	v, _ := metaValue(t, db, "schema_version")
	require.Equal(t, "4", v, "v3->v4 migration must update schema_version")

	cols := columnsOf(t, db, "sync_events")
	require.Contains(t, cols, "uid", "v3->v4 must add sync_events.uid")

	// Pre-existing rows survive (uid is NULL for the legacy row — tolerated).
	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM sync_events").Scan(&n))
	require.Equal(t, 1, n, "legacy sync_events rows survive the v3->v4 ALTER")
}

// TestSchema_V4MigrationIdempotent re-opens a v4 DB and asserts the
// migration step is a tolerant no-op (no duplicate-column hard error).
func TestSchema_V4MigrationIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "idem4.db")
	s1, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s1.Close())

	s2, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })

	v, _ := metaValue(t, raw, "schema_version")
	require.Equal(t, "4", v)
}
