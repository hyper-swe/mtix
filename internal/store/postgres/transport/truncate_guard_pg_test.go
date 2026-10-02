// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// seedAppendOnlyRows writes one row into audit_log, sync_events and
// sync_conflicts as the pool's role, so a TRUNCATE has rows to remove.
func seedAppendOnlyRows(t *testing.T, pool *transport.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, stmt := range []string{
		`INSERT INTO audit_log (project_prefix, actor, action) VALUES ('TST', 'tester', 'seed')`,
		`INSERT INTO sync_events (event_id, project_prefix, node_id, op_type, payload,
			wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash)
		 VALUES ('ev-a', 'TST', 'TST-1', 'create_node', '{}', 1, 1, '{}', 'tester', 'abcdef0123456789'),
		        ('ev-b', 'TST', 'TST-1', 'update_field', '{}', 2, 2, '{}', 'tester', 'abcdef0123456789')`,
		`INSERT INTO sync_conflicts (event_id_a, event_id_b, node_id, field_name, resolution)
		 VALUES ('ev-a', 'ev-b', 'TST-1', 'title', 'lww')`,
	} {
		_, err := pool.Inner().Exec(ctx, stmt)
		require.NoError(t, err)
	}
}

// countTableRows returns the row count of one of the fixed append-only
// tables. The table name is chosen from a constant set, never user input.
func countTableRows(t *testing.T, pool *transport.Pool, table string) int {
	t.Helper()
	queries := map[string]string{
		"audit_log":      `SELECT count(*) FROM audit_log`,
		"sync_conflicts": `SELECT count(*) FROM sync_conflicts`,
		"sync_events":    `SELECT count(*) FROM sync_events`,
	}
	q, ok := queries[table]
	require.True(t, ok, table)
	var n int
	require.NoError(t, pool.Inner().QueryRow(context.Background(), q).Scan(&n))
	return n
}

// guardCreations lists the TRUNCATE guards the recorder saw created.
const guardCreations = `SELECT identity FROM mtixt_audit.created_triggers
	WHERE identity LIKE '%\_no\_truncate on %' ORDER BY identity`

// TestTruncateGuard_BlocksOwnerTruncate: each append-only table also has a
// statement-level TRUNCATE guard (migration 016). The owner's TRUNCATE on
// each, alone and with CASCADE, raises and keeps every row, and a second
// migrate creates no guard trigger (MTIX-95.1, F-44). sync_events is
// referenced by foreign keys, so PostgreSQL itself refuses to truncate it
// alone; its cases name it first, so its own guard is the one that raises
// (the message names the table).
func TestTruncateGuard_BlocksOwnerTruncate(t *testing.T) {
	f := newHubFixture(t)
	owner := f.ownerRole()
	installDDLRecorder(f)
	pool := f.openAs(owner, transport.Options{})
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))
	require.Equal(t, []string{
		"audit_log_no_truncate on public.audit_log",
		"sync_conflicts_no_truncate on public.sync_conflicts",
		"sync_events_no_truncate on public.sync_events",
	}, f.queryStrings(guardCreations), "the recorder sees the first migrate create each guard")
	seedAppendOnlyRows(t, pool)

	tests := []struct {
		name  string
		table string
		stmt  string
	}{
		{"audit_log", "audit_log", `TRUNCATE audit_log`},
		{"audit_log cascade", "audit_log", `TRUNCATE audit_log CASCADE`},
		{"sync_conflicts", "sync_conflicts", `TRUNCATE sync_conflicts`},
		{"sync_conflicts cascade", "sync_conflicts", `TRUNCATE sync_conflicts CASCADE`},
		{"sync_events cascade", "sync_events", `TRUNCATE sync_events CASCADE`},
		{"sync_events with its dependants", "sync_events",
			`TRUNCATE sync_events, sync_conflicts, node_renumber_remaps, sync_node_collisions`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := countTableRows(t, pool, tt.table)
			require.Positive(t, before)
			_, err := pool.Inner().Exec(ctx, tt.stmt)
			require.Error(t, err, "TRUNCATE must be refused")
			require.Contains(t, err.Error(), "TRUNCATE forbidden")
			require.Contains(t, err.Error(), "append-only table "+tt.table+":",
				"the guard on %s itself raises", tt.table)
			require.Equal(t, before, countTableRows(t, pool, tt.table), "rows are kept")
		})
	}

	f.exec(`DELETE FROM mtixt_audit.created_triggers`)
	require.NoError(t, pool.Migrate(ctx))
	require.Empty(t, f.queryStrings(guardCreations), "a second migrate creates no guard trigger")
}

// installDDLRecorder makes the fixture database record the object identity
// of every CREATE TRIGGER that completes, through a superuser-owned event
// trigger, so a test can see which triggers a migrate created.
func installDDLRecorder(f *hubFixture) {
	f.exec(`CREATE SCHEMA mtixt_audit`)
	f.exec(`CREATE TABLE mtixt_audit.created_triggers (identity TEXT NOT NULL)`)
	f.exec(`CREATE FUNCTION mtixt_audit.record_triggers() RETURNS event_trigger
		LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
		BEGIN
			INSERT INTO mtixt_audit.created_triggers (identity)
			SELECT object_identity FROM pg_event_trigger_ddl_commands()
			WHERE command_tag = 'CREATE TRIGGER';
		END $$`)
	f.exec(`CREATE EVENT TRIGGER mtixt_record_triggers ON ddl_command_end
		WHEN TAG IN ('CREATE TRIGGER') EXECUTE FUNCTION mtixt_audit.record_triggers()`)
}
