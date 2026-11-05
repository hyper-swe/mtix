// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// newStateTestDB creates an in-memory SQLite with the minimal schema
// required by DetectState — meta + applied_events + sync_events +
// sync_conflicts. The real package-wide schema lives in
// internal/store/sqlite; we duplicate the minimum here so the
// workflow package has no dependency on the sqlite store package
// (and so tests do not pull in the full migration chain).
func newStateTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&mode=memory&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	stmts := []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE sync_events (
			event_id TEXT PRIMARY KEY,
			node_id TEXT NOT NULL,
			op_type TEXT NOT NULL,
			lamport INTEGER NOT NULL,
			wall_clock_ts TEXT NOT NULL,
			author_machine_hash TEXT NOT NULL,
			payload TEXT NOT NULL,
			sync_status TEXT NOT NULL DEFAULT 'pending',
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE applied_events (
			event_id TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE sync_conflicts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			detected_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			resolved_at TEXT
		)`,
		`INSERT INTO meta (key, value) VALUES ('meta.sync.machine_hash', '')`,
		`INSERT INTO meta (key, value) VALUES ('meta.sync.consecutive_errors', '0')`,
	}
	for _, s := range stmts {
		_, err := db.ExecContext(ctx, s)
		require.NoErrorf(t, err, "exec: %s", s)
	}
	return db
}

// setMeta updates a meta value, inserting if absent.
func setMeta(t *testing.T, db *sql.DB, key, value string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	require.NoError(t, err)
}

func TestDetectState_Solo_NoDSN_NoActivity(t *testing.T) {
	db := newStateTestDB(t)
	t.Setenv("MTIX_SYNC_DSN", "")

	mtixDir := t.TempDir()
	report, err := DetectState(context.Background(), db, mtixDir)
	require.NoError(t, err)
	require.Equal(t, StateSolo, report.State)
	require.False(t, report.HasDSN)
	require.False(t, report.HasUnresolvedConflicts)
	require.Equal(t, 0, report.LocalEventCount)
	require.Equal(t, 0, report.AppliedEventCount)
	require.Equal(t, 0, report.ConsecutiveErrors)
}

func TestDetectState_SyncConfiguredNoHub_DSNSetButNeverPushed(t *testing.T) {
	db := newStateTestDB(t)
	t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")

	mtixDir := t.TempDir()
	report, err := DetectState(context.Background(), db, mtixDir)
	require.NoError(t, err)
	require.Equal(t, StateSyncConfiguredNoHub, report.State)
	require.True(t, report.HasDSN)
}

func TestDetectState_SyncActive_DSNSetAndEventsExist(t *testing.T) {
	db := newStateTestDB(t)
	t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")

	setMeta(t, db, "meta.sync.machine_hash", "abc123")
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO sync_events (event_id, node_id, op_type, lamport, wall_clock_ts,
			author_machine_hash, payload) VALUES
		 ('e1', 'PROJ-1', 'create_node', 1, '2026-05-01T00:00:00Z', 'abc123', '{}')`)
	require.NoError(t, err)

	mtixDir := t.TempDir()
	report, err := DetectState(context.Background(), db, mtixDir)
	require.NoError(t, err)
	require.Equal(t, StateSyncActive, report.State)
	require.True(t, report.HasDSN)
	require.Equal(t, 1, report.LocalEventCount)
}

func TestDetectState_DivergentStatePending_UnresolvedConflicts(t *testing.T) {
	db := newStateTestDB(t)
	t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")

	setMeta(t, db, "meta.sync.machine_hash", "abc123")
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO sync_conflicts (event_id, node_id, resolved_at) VALUES ('e1', 'PROJ-1', NULL)`)
	require.NoError(t, err)

	mtixDir := t.TempDir()
	report, err := DetectState(context.Background(), db, mtixDir)
	require.NoError(t, err)
	require.Equal(t, StateDivergentPending, report.State)
	require.True(t, report.HasUnresolvedConflicts)
}

func TestDetectState_DivergentStatePending_OvertakesSyncActive(t *testing.T) {
	// If both unresolved conflicts AND active sync are present, divergent
	// takes priority — the agent must surface the conflict first.
	db := newStateTestDB(t)
	t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")

	setMeta(t, db, "meta.sync.machine_hash", "abc123")
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO sync_events (event_id, node_id, op_type, lamport, wall_clock_ts,
			author_machine_hash, payload) VALUES
		 ('e1', 'PROJ-1', 'create_node', 1, '2026-05-01T00:00:00Z', 'abc123', '{}')`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO sync_conflicts (event_id, node_id, resolved_at) VALUES ('e2', 'PROJ-1', NULL)`)
	require.NoError(t, err)

	mtixDir := t.TempDir()
	report, err := DetectState(context.Background(), db, mtixDir)
	require.NoError(t, err)
	require.Equal(t, StateDivergentPending, report.State)
}

func TestDetectState_DivergentStatePending_ResolvedConflictsIgnored(t *testing.T) {
	// Resolved conflicts (resolved_at NOT NULL) must not flip state to divergent.
	db := newStateTestDB(t)
	t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")

	setMeta(t, db, "meta.sync.machine_hash", "abc123")
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO sync_events (event_id, node_id, op_type, lamport, wall_clock_ts,
			author_machine_hash, payload) VALUES
		 ('e1', 'PROJ-1', 'create_node', 1, '2026-05-01T00:00:00Z', 'abc123', '{}')`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO sync_conflicts (event_id, node_id, resolved_at) VALUES ('e2', 'PROJ-1', '2026-05-02T00:00:00Z')`)
	require.NoError(t, err)

	mtixDir := t.TempDir()
	report, err := DetectState(context.Background(), db, mtixDir)
	require.NoError(t, err)
	require.Equal(t, StateSyncActive, report.State)
	require.False(t, report.HasUnresolvedConflicts)
}

func TestDetectState_HubUnreachable_ConsecutiveErrorsAtThreshold(t *testing.T) {
	db := newStateTestDB(t)
	t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")

	setMeta(t, db, "meta.sync.machine_hash", "abc123")
	setMeta(t, db, "meta.sync.consecutive_errors", "3")
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO sync_events (event_id, node_id, op_type, lamport, wall_clock_ts,
			author_machine_hash, payload) VALUES
		 ('e1', 'PROJ-1', 'create_node', 1, '2026-05-01T00:00:00Z', 'abc123', '{}')`)
	require.NoError(t, err)

	mtixDir := t.TempDir()
	report, err := DetectState(context.Background(), db, mtixDir)
	require.NoError(t, err)
	require.Equal(t, StateHubUnreachable, report.State)
	require.Equal(t, 3, report.ConsecutiveErrors)
}

func TestDetectState_HubUnreachable_BelowThresholdStaysActive(t *testing.T) {
	db := newStateTestDB(t)
	t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")

	setMeta(t, db, "meta.sync.machine_hash", "abc123")
	setMeta(t, db, "meta.sync.consecutive_errors", "2")
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO sync_events (event_id, node_id, op_type, lamport, wall_clock_ts,
			author_machine_hash, payload) VALUES
		 ('e1', 'PROJ-1', 'create_node', 1, '2026-05-01T00:00:00Z', 'abc123', '{}')`)
	require.NoError(t, err)

	mtixDir := t.TempDir()
	report, err := DetectState(context.Background(), db, mtixDir)
	require.NoError(t, err)
	require.Equal(t, StateSyncActive, report.State)
	require.Equal(t, 2, report.ConsecutiveErrors)
}

func TestDetectState_NeverSurfacesRawDSN(t *testing.T) {
	// FR-18.17 regression: the report struct must never contain DSN bytes.
	db := newStateTestDB(t)
	const sentinel = "SECRET_DO_NOT_LEAK_BBC571"
	t.Setenv("MTIX_SYNC_DSN", "postgres://u:"+sentinel+"@host/db")

	mtixDir := t.TempDir()
	report, err := DetectState(context.Background(), db, mtixDir)
	require.NoError(t, err)

	body, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContainsf(t, string(body), sentinel,
		"DetectState report leaked DSN sentinel: %s", string(body))
}

func TestDetectState_DSNFromSecretsFile(t *testing.T) {
	// HasDSN must be true when only the .mtix/secrets file is set.
	db := newStateTestDB(t)
	t.Setenv("MTIX_SYNC_DSN", "")

	mtixDir := t.TempDir()
	secretsPath := filepath.Join(mtixDir, "secrets")
	require.NoError(t, writeSecretsFile(t, secretsPath, "postgres://u:p@h/d"))

	report, err := DetectState(context.Background(), db, mtixDir)
	require.NoError(t, err)
	require.True(t, report.HasDSN, "DSN from secrets file should be detected")
}

func TestDetectState_AppliedEventsAlone_IsSyncActive(t *testing.T) {
	// Receiving events from the hub (applied_events > 0) without ever
	// emitting locally still counts as sync-active — the user has a working
	// pull path.
	db := newStateTestDB(t)
	t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")

	setMeta(t, db, "meta.sync.machine_hash", "abc123")
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO applied_events (event_id) VALUES ('e1')`)
	require.NoError(t, err)

	mtixDir := t.TempDir()
	report, err := DetectState(context.Background(), db, mtixDir)
	require.NoError(t, err)
	require.Equal(t, StateSyncActive, report.State)
	require.Equal(t, 1, report.AppliedEventCount)
}

func TestState_String_StableNames(t *testing.T) {
	// The string forms are part of the MCP tool's contract; pin them.
	got := []string{
		StateSolo.String(),
		StateSyncConfiguredNoHub.String(),
		StateSyncActive.String(),
		StateDivergentPending.String(),
		StateHubUnreachable.String(),
	}
	want := []string{
		"solo",
		"sync-configured-no-hub",
		"sync-active",
		"divergent-state-pending",
		"hub-unreachable",
	}
	require.Equal(t, want, got)
}

func writeSecretsFile(t *testing.T, path, body string) error {
	t.Helper()
	return os.WriteFile(path, []byte(body), 0o600)
}
