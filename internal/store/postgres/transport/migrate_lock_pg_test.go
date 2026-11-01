// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Inspect the actual Migrate transaction immediately before its commit.
// Set MTIX_PG_MIGRATION_ROWS=2000000 (or higher) to measure multi-second builds.
func TestMigrate_CurrentHubPullDoesNotWaitForCommit(t *testing.T) {
	t.Run("current", func(t *testing.T) { assertMigrationPullBeforeCommit(t, false) })
	t.Run("build_indexes", func(t *testing.T) { assertMigrationPullBeforeCommit(t, true) })
}

func assertMigrationPullBeforeCommit(t *testing.T, rebuild bool) {
	t.Helper()
	f := newHubFixture(t)
	pool := f.openAs(f.ownerRole(), transport.Options{})
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))
	seedMigrationRows(t, f)
	if rebuild {
		f.exec(`DROP INDEX idx_sync_events_created_at, idx_sync_events_lamport_event_id`)
	}
	calls := 0
	transport.SetBeforeMigrateCommitForTest(pool, func(tx pgx.Tx) error {
		calls++
		var indexesReady bool
		require.NoError(t, tx.QueryRow(ctx, `SELECT
			to_regclass('idx_sync_events_created_at') IS NOT NULL
			AND to_regclass('idx_sync_events_lamport_event_id') IS NOT NULL`).Scan(&indexesReady))
		require.True(t, indexesReady, "the entire migration batch precedes the commit check")
		var exclusive int
		require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM pg_locks
			WHERE pid=pg_backend_pid() AND relation IN (
				SELECT oid FROM pg_class WHERE relkind IN ('r','p')
				AND relnamespace=current_schema()::regnamespace)
			AND mode='AccessExclusiveLock' AND granted`).Scan(&exclusive))
		pullCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var count int
		pullErr := f.admin.QueryRow(pullCtx, `SELECT count(*) FROM
			(SELECT event_id FROM sync_events WHERE project_prefix=$1 LIMIT 1) events`, "TEST").Scan(&count)
		require.NoError(t, pullErr, "pull SELECT must finish before migration commit; exclusive locks=%d", exclusive)
		require.Zero(t, exclusive, "a current hub needs no ACCESS EXCLUSIVE table lock")
		return nil
	})
	defer transport.SetBeforeMigrateCommitForTest(pool, nil)
	started := time.Now()
	require.NoError(t, pool.Migrate(ctx))
	require.Equal(t, 1, calls, "the commit check must actually execute once")
	t.Logf("Migrate with the transaction open during lock and pull checks: %s", time.Since(started))
}

func seedMigrationRows(t *testing.T, f *hubFixture) {
	t.Helper()
	rows := 100
	if value := os.Getenv("MTIX_PG_MIGRATION_ROWS"); value != "" {
		var err error
		rows, err = strconv.Atoi(value)
		require.NoError(t, err)
		require.Positive(t, rows)
	}
	_, err := f.admin.Exec(context.Background(), `INSERT INTO sync_events
		(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,
			vector_clock,author_id,author_machine_hash)
		SELECT 'TEST-' || n, 'TEST', 'TEST-1', 'update_field', '{}', n, n,
			'{}', 'TEST', 'TEST' FROM generate_series(1,$1::int) n`, rows)
	require.NoError(t, err)
}

func TestMigrate_ColumnGuardsPreserveReleasedSchema(t *testing.T) {
	legacy := newHubFixture(t)
	guarded := newHubFixture(t)
	for _, f := range []*hubFixture{legacy, guarded} {
		body, err := migrations.Read("001_sync_events.sql")
		require.NoError(t, err)
		f.exec(body)
		seedMigrationRows(t, f)
	}
	// Exact released column definitions, index, singleton table and seed.
	legacy.exec(`ALTER TABLE sync_events ADD COLUMN IF NOT EXISTS uid TEXT;
		CREATE INDEX IF NOT EXISTS idx_sync_events_uid ON sync_events (uid)
			WHERE uid IS NOT NULL AND uid <> '';
		CREATE TABLE IF NOT EXISTS sync_hub_state (
			id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
			restore_epoch BIGINT NOT NULL DEFAULT 0);
		INSERT INTO sync_hub_state (id,restore_epoch) VALUES (TRUE,0) ON CONFLICT (id) DO NOTHING;
		ALTER TABLE sync_events ADD COLUMN IF NOT EXISTS restore_epoch BIGINT NOT NULL DEFAULT 0`)
	for _, name := range []string{"010_sync_events_uid.sql", "013_hub_restore_epoch.sql"} {
		body, err := migrations.Read(name)
		require.NoError(t, err)
		guarded.exec(body)
	}
	require.Equal(t, migrationColumnSnapshot(legacy), migrationColumnSnapshot(guarded), "fresh legacy rows have identical definitions and backfill")
	for _, f := range []*hubFixture{legacy, guarded} {
		f.exec(`UPDATE sync_events SET uid='TEST-uid', restore_epoch=7;
			UPDATE sync_hub_state SET restore_epoch=9`)
		before := migrationColumnSnapshot(f)
		for _, name := range []string{"010_sync_events_uid.sql", "013_hub_restore_epoch.sql"} {
			body, err := migrations.Read(name)
			require.NoError(t, err)
			f.exec(body)
		}
		require.Equal(t, before, migrationColumnSnapshot(f), "existing values and epoch survive replay")
	}
}

func migrationColumnSnapshot(f *hubFixture) []string {
	return f.queryStrings(`SELECT 'column:' || attname || ':' || format_type(atttypid,atttypmod)
		|| ':' || attnotnull || ':' || COALESCE(pg_get_expr(d.adbin,d.adrelid),'')
		FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
		WHERE a.attrelid='sync_events'::regclass AND attnum>0 AND NOT attisdropped
		UNION ALL SELECT 'index:' || indexdef FROM pg_indexes WHERE tablename='sync_events'
		UNION ALL SELECT 'data:' || event_id || ':' || COALESCE(uid,'NULL') || ':' || restore_epoch FROM sync_events
		UNION ALL SELECT 'epoch:' || restore_epoch FROM sync_hub_state ORDER BY 1`)
}

func TestMigrate_ColumnGuardsRestoreDroppedColumns(t *testing.T) {
	f := newHubFixture(t)
	body, err := migrations.Read("001_sync_events.sql")
	require.NoError(t, err)
	f.exec(body)
	seedMigrationRows(t, f)
	for attempt := 0; attempt < 2; attempt++ {
		for _, name := range []string{"010_sync_events_uid.sql", "013_hub_restore_epoch.sql"} {
			body, err = migrations.Read(name)
			require.NoError(t, err)
			f.exec(body)
		}
		require.Equal(t, []string{"true"}, f.queryStrings(`SELECT
			bool_and(uid IS NULL AND restore_epoch=0)::text FROM sync_events`))
		if attempt == 0 {
			f.exec(`ALTER TABLE sync_events DROP COLUMN uid;
				ALTER TABLE sync_events DROP COLUMN restore_epoch`)
		}
	}
}

func TestMigrate_TriggerGuardsPreserveRepairBehavior(t *testing.T) {
	for name, mutation := range migrationTriggerMutations() {
		t.Run(name, func(t *testing.T) {
			f := newHubFixture(t)
			pool := f.openAs(f.ownerRole(), transport.Options{})
			require.NoError(t, pool.Migrate(context.Background()))
			before := migrationTriggerSnapshot(f)
			f.exec(mutation)
			require.NoError(t, pool.Migrate(context.Background()))
			require.Equal(t, before, migrationTriggerSnapshot(f), "replay must repair exactly as released006 did")
		})
	}
}

// Each fixture is valid PostgreSQL DDL; a fresh database isolates mutations.
func migrationTriggerMutations() map[string]string {
	return map[string]string{
		"disabled": `ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_update`,
		"missing":  `DROP TRIGGER audit_log_no_delete ON audit_log`,
		"conditional": `DROP TRIGGER sync_conflicts_no_delete ON sync_conflicts;
			CREATE TRIGGER sync_conflicts_no_delete BEFORE DELETE ON sync_conflicts
			FOR EACH ROW WHEN (OLD.node_id = 'TEST-1') EXECUTE FUNCTION audit_log_immutable()`,
		"column_specific": `DROP TRIGGER sync_conflicts_no_update ON sync_conflicts;
			CREATE TRIGGER sync_conflicts_no_update BEFORE UPDATE OF node_id ON sync_conflicts
			FOR EACH ROW EXECUTE FUNCTION audit_log_immutable()`,
		"arguments": `DROP TRIGGER audit_log_no_update ON audit_log;
			CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
			FOR EACH ROW EXECUTE FUNCTION audit_log_immutable('TEST')`,
		"wrong_function": `DROP TRIGGER audit_log_no_update ON audit_log;
			CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
			FOR EACH ROW EXECUTE FUNCTION append_only_no_truncate()`,
		"wrong_event": `DROP TRIGGER audit_log_no_update ON audit_log;
			CREATE TRIGGER audit_log_no_update BEFORE INSERT ON audit_log
			FOR EACH ROW EXECUTE FUNCTION audit_log_immutable()`,
		"wrong_timing": `DROP TRIGGER audit_log_no_update ON audit_log;
			CREATE TRIGGER audit_log_no_update AFTER UPDATE ON audit_log
			FOR EACH ROW EXECUTE FUNCTION audit_log_immutable()`,
		"constraint": `DROP TRIGGER audit_log_no_update ON audit_log;
			CREATE CONSTRAINT TRIGGER audit_log_no_update AFTER UPDATE ON audit_log
			DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION audit_log_immutable()`,
	}
}

func migrationTriggerSnapshot(f *hubFixture) []string {
	return f.queryStrings(`SELECT tgname || ':' || tgenabled::text || ':' || pg_get_triggerdef(oid)
			FROM pg_trigger WHERE tgrelid IN ('audit_log'::regclass,'sync_conflicts'::regclass)
			AND NOT tgisinternal ORDER BY tgname`)
}
