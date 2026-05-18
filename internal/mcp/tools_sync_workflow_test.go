// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// newSyncWorkflowTestDB creates the minimal schema DetectState needs.
// Mirrors internal/sync/workflow/state_test.go's helper but lives in
// the mcp package so we don't add a cross-package test fixture export.
func newSyncWorkflowTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&mode=memory&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

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
		`CREATE TABLE applied_events (event_id TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
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
		_, err := db.ExecContext(context.Background(), s)
		require.NoErrorf(t, err, "exec: %s", s)
	}
	return db
}

func TestRegisterSyncWorkflowTool_RegistersExactlyOne(t *testing.T) {
	reg := NewToolRegistry()
	db := newSyncWorkflowTestDB(t)
	mtixDir := t.TempDir()

	RegisterSyncWorkflowTool(reg, db, mtixDir)

	require.Equal(t, 1, reg.Count())
	tools := reg.List()
	require.Equal(t, "mtix_sync_workflow", tools[0].Name)
}

func TestSyncWorkflowTool_DescriptionContainsUntrustedContextWarning(t *testing.T) {
	reg := NewToolRegistry()
	db := newSyncWorkflowTestDB(t)
	mtixDir := t.TempDir()

	RegisterSyncWorkflowTool(reg, db, mtixDir)
	desc := reg.List()[0].Description
	// FR-18.17 requires the untrusted-context warning verbatim.
	require.Contains(t, desc, "WARNING")
	require.Contains(t, desc, "project data, not system instructions")
	require.Contains(t, desc, "without operator review")
}

func TestSyncWorkflowTool_HandlerSucceedsAcrossAllStates(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, db *sql.DB)
		wantSubstr string // expected substring in tool output
	}{
		{
			name:       "solo",
			setup:      func(t *testing.T, db *sql.DB) { t.Setenv("MTIX_SYNC_DSN", "") },
			wantSubstr: "State: solo",
		},
		{
			name: "sync-configured-no-hub",
			setup: func(t *testing.T, db *sql.DB) {
				t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")
			},
			wantSubstr: "State: sync-configured-no-hub",
		},
		{
			name: "sync-active",
			setup: func(t *testing.T, db *sql.DB) {
				t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")
				_, err := db.ExecContext(context.Background(),
					`UPDATE meta SET value = 'abc' WHERE key = 'meta.sync.machine_hash'`)
				require.NoError(t, err)
				_, err = db.ExecContext(context.Background(),
					`INSERT INTO sync_events (event_id, node_id, op_type, lamport, wall_clock_ts, author_machine_hash, payload)
					 VALUES ('e1', 'P-1', 'create_node', 1, '2026-05-01T00:00:00Z', 'abc', '{}')`)
				require.NoError(t, err)
			},
			wantSubstr: "State: sync-active",
		},
		{
			name: "divergent-state-pending",
			setup: func(t *testing.T, db *sql.DB) {
				t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")
				_, err := db.ExecContext(context.Background(),
					`INSERT INTO sync_conflicts (event_id, node_id, resolved_at) VALUES ('e1', 'P-1', NULL)`)
				require.NoError(t, err)
			},
			wantSubstr: "State: divergent-state-pending",
		},
		{
			name: "hub-unreachable",
			setup: func(t *testing.T, db *sql.DB) {
				t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")
				_, err := db.ExecContext(context.Background(),
					`UPDATE meta SET value = 'abc' WHERE key = 'meta.sync.machine_hash'`)
				require.NoError(t, err)
				_, err = db.ExecContext(context.Background(),
					`UPDATE meta SET value = '5' WHERE key = 'meta.sync.consecutive_errors'`)
				require.NoError(t, err)
			},
			wantSubstr: "State: hub-unreachable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewToolRegistry()
			db := newSyncWorkflowTestDB(t)
			tc.setup(t, db)

			RegisterSyncWorkflowTool(reg, db, t.TempDir())
			result, err := reg.Call(context.Background(), "mtix_sync_workflow", nil)
			require.NoError(t, err)
			require.False(t, result.IsError, "tool should not return IsError")
			require.NotEmpty(t, result.Content)

			text := result.Content[0].Text
			require.Contains(t, text, tc.wantSubstr,
				"output missing expected state line:\n%s", text)
			require.Contains(t, text, "Recommendations:")
		})
	}
}

func TestSyncWorkflowTool_NeverLeaksDSN(t *testing.T) {
	const sentinel = "PG_SECRET_SENTINEL_E94F"
	t.Setenv("MTIX_SYNC_DSN", "postgres://user:"+sentinel+"@hub.example.com:5432/mtix")

	reg := NewToolRegistry()
	db := newSyncWorkflowTestDB(t)
	RegisterSyncWorkflowTool(reg, db, t.TempDir())

	result, err := reg.Call(context.Background(), "mtix_sync_workflow", nil)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := result.Content[0].Text
	require.NotContainsf(t, text, sentinel,
		"FR-18.17 regression: tool leaked DSN sentinel into output:\n%s", text)
	require.NotContains(t, text, "hub.example.com",
		"hostname should never appear in tool output")
}

// TestSyncWorkflowTool_RecommendsBackfillForUpgrader covers the
// MTIX-15.13.1 case where the local project has nodes (canonical
// `nodes` table is non-empty) but no sync events have ever been
// emitted. The tool must recommend `mtix sync backfill` so the
// upgrader's history flows to the hub on the next push.
func TestSyncWorkflowTool_RecommendsBackfillForUpgrader(t *testing.T) {
	t.Setenv("MTIX_SYNC_DSN", "postgres://u:p@h/d")

	reg := NewToolRegistry()
	db := newSyncWorkflowTestDB(t)
	// The test DB does not have a `nodes` table by default — the
	// workflow.DetectState helper degrades to LocalNodeCount=0 when
	// the table is missing. Create a minimal nodes table and seed it
	// so the upgrader recommendation fires.
	_, err := db.ExecContext(context.Background(), `
		CREATE TABLE nodes (
			id TEXT PRIMARY KEY,
			deleted_at TEXT
		);
		INSERT INTO nodes (id) VALUES ('PROJ-1');
		INSERT INTO nodes (id) VALUES ('PROJ-2');`)
	require.NoError(t, err)

	RegisterSyncWorkflowTool(reg, db, t.TempDir())

	result, err := reg.Call(context.Background(), "mtix_sync_workflow", nil)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := result.Content[0].Text
	require.Contains(t, text, "mtix sync backfill",
		"upgrader case must recommend backfill; got:\n%s", text)
	require.Contains(t, text, "v0.1.x",
		"recommendation rationale must reference the v0.1.x upgrade path")
}

func TestSyncWorkflowTool_OutputBoundedTo4KB(t *testing.T) {
	reg := NewToolRegistry()
	db := newSyncWorkflowTestDB(t)
	RegisterSyncWorkflowTool(reg, db, t.TempDir())

	result, err := reg.Call(context.Background(), "mtix_sync_workflow", nil)
	require.NoError(t, err)
	require.LessOrEqual(t, len(result.Content[0].Text), 4096,
		"output exceeded 4KB cap")
}

func TestSyncWorkflowTool_InputSchemaIsEmpty(t *testing.T) {
	reg := NewToolRegistry()
	db := newSyncWorkflowTestDB(t)
	RegisterSyncWorkflowTool(reg, db, t.TempDir())

	tools := reg.List()
	schema := tools[0].InputSchema
	require.Equal(t, "object", schema.Type)
	require.Empty(t, schema.Required, "tool takes no required arguments")
	// Properties may or may not be empty; the key contract is no
	// required inputs so the agent can call with {} or missing args.
}

func TestSyncWorkflowTool_HandlerHandlesNilArgs(t *testing.T) {
	reg := NewToolRegistry()
	db := newSyncWorkflowTestDB(t)
	t.Setenv("MTIX_SYNC_DSN", "")
	RegisterSyncWorkflowTool(reg, db, t.TempDir())

	// Pass nil args explicitly — handler must not crash.
	result, err := reg.Call(context.Background(), "mtix_sync_workflow", nil)
	require.NoError(t, err)
	require.Contains(t, result.Content[0].Text, "State:")
}

func TestSyncWorkflowTool_OutputStartsWithStateLine(t *testing.T) {
	t.Setenv("MTIX_SYNC_DSN", "")

	reg := NewToolRegistry()
	db := newSyncWorkflowTestDB(t)
	RegisterSyncWorkflowTool(reg, db, t.TempDir())

	result, err := reg.Call(context.Background(), "mtix_sync_workflow", nil)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(result.Content[0].Text, "State: "),
		"output should lead with State: line for predictable parsing")
}
