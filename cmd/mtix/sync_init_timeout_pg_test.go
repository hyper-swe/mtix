// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// A server-side pause in CREATE INDEX gives a deterministic small-budget
// regression without requiring a 30-second test on every developer run.
func TestSyncInit_IndexMigrationDeadlineAndRetry(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner := f.ownerRole()
	f.migrateAs(owner)
	f.exec(`DROP INDEX idx_sync_events_created_at, idx_sync_events_lamport_event_id`)
	f.exec(`CREATE FUNCTION mtixt_delay_index() RETURNS event_trigger LANGUAGE plpgsql AS $$
  BEGIN PERFORM pg_sleep(0.1); END $$;
  CREATE EVENT TRIGGER mtixt_delay_index ON ddl_command_start
   WHEN TAG IN ('CREATE INDEX') EXECUTE FUNCTION mtixt_delay_index()`)
	t.Setenv(transport.EnvDSN, f.dsnAs(owner))
	var stdout, stderr bytes.Buffer
	cmd := newSyncInitCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--insecure-tls", "--migrate-timeout=30ms"})
	err := cmd.ExecuteContext(context.Background())
	require.ErrorContains(t, err, "--migrate-timeout=30ms")
	require.ErrorContains(t, err, "mtix sync init --migrate-timeout=15m")
	require.ErrorContains(t, err, "then run mtix sync doctor")
	require.Equal(t, []string{"false"}, f.strings(`SELECT
  (to_regclass('idx_sync_events_created_at') IS NOT NULL)::text`))
	cmd = newSyncInitCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--insecure-tls", "--migrate-timeout=5s"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	require.Equal(t, []string{"true", "true"}, f.strings(`SELECT indisvalid::text
  FROM pg_index WHERE indexrelid IN ('idx_sync_events_created_at'::regclass,
   'idx_sync_events_lamport_event_id'::regclass)`))
	require.Contains(t, stdout.String(), "hub schema migrated")
}

// This slow PG case catches accidentally retaining the expired 30s connect
// context for either migration or the registration that follows it.
func TestSyncInit_IndexMigrationOutlivesConnectBudget(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner := f.ownerRole()
	f.migrateAs(owner)
	f.exec(`DROP INDEX idx_sync_events_created_at`)
	f.exec(`CREATE FUNCTION mtixt_delay_built_index() RETURNS event_trigger LANGUAGE plpgsql AS $$
  BEGIN
   IF EXISTS (SELECT 1 FROM pg_event_trigger_ddl_commands()
    WHERE object_identity='public.idx_sync_events_created_at') THEN
    PERFORM pg_sleep(31);
   END IF;
  END $$;
  CREATE EVENT TRIGGER mtixt_delay_built_index ON ddl_command_end
   WHEN TAG IN ('CREATE INDEX') EXECUTE FUNCTION mtixt_delay_built_index()`)
	require.NoError(t, runCreate("migration deadline test", "", "", 3, "", "", "", "", ""))
	t.Setenv(transport.EnvDSN, f.dsnAs(owner))
	var stdout, stderr bytes.Buffer
	cmd := newSyncInitCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--insecure-tls", "--migrate-timeout=60s"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	require.Contains(t, stdout.String(), "registered on hub")
	require.Equal(t, []string{"true"}, f.strings(`SELECT indisvalid::text
  FROM pg_index WHERE indexrelid='idx_sync_events_created_at'::regclass`))
}
