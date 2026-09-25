// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDoctorHubTriggers_MissingOrDisabled_NamesEachAndTheExactFix: the
// doctor's hub-triggers check compares the hub with the function and
// trigger sets the migrations define. A missing trigger and a disabled
// one (tgenabled other than 'O') are named with the fix: mtix sync init
// for what is missing, and the ALTER TABLE statement for what is
// disabled. It is a WARN by default and a FAIL in strict mode; once the
// fix is applied it passes (MTIX-95.7).
func TestDoctorHubTriggers_MissingOrDisabled_NamesEachAndTheExactFix(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	pool := hubPool(t, dsn)

	report, err := runDoctorReport(t)
	require.NoError(t, err)
	pass, warn, detail, fix := doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, pass, detail)
	require.False(t, warn, "a freshly migrated hub has every function and trigger: %s", detail)
	require.Empty(t, fix)

	for _, stmt := range []string{
		`ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_update`,
		`ALTER TABLE sync_events ENABLE ALWAYS TRIGGER sync_events_no_truncate`,
		`DROP TRIGGER sync_conflicts_no_delete ON sync_conflicts`,
	} {
		_, err := pool.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	report, err = runDoctorReport(t)
	require.NoError(t, err, "a WARN keeps the doctor's exit code 0")
	pass, warn, detail, fix = doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, pass)
	require.True(t, warn)
	require.Contains(t, detail, "missing triggers: sync_conflicts_no_delete on public.sync_conflicts")
	require.Contains(t, detail, "audit_log_no_update on public.audit_log (tgenabled D)")
	require.NotContains(t, detail, "sync_events_no_truncate", "ENABLE ALWAYS (tgenabled A) counts as enabled, as for harden")
	require.Contains(t, fix, "mtix sync init")
	require.Contains(t, fix, "ALTER TABLE public.audit_log ENABLE TRIGGER audit_log_no_update;")
	require.NotContains(t, fix, "sync_events_no_truncate", "the fix never turns an ALWAYS trigger into an ordinary one")

	setKeepRoles(t, "mtix_team")
	report, err = runDoctorReport(t)
	require.ErrorIs(t, err, errDoctorChecksFailed)
	pass, warn, _, _ = doctorCheckNamed(t, report, "hub-triggers")
	require.False(t, pass, "strict mode fails the check")
	require.False(t, warn)
	setKeepRoles(t, "")

	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncInit(ctx, &stdout, &stderr, nil, cloudOpts), stderr.String())
	_, err = pool.Exec(ctx, `ALTER TABLE public.audit_log ENABLE TRIGGER audit_log_no_update;`)
	require.NoError(t, err)
	report, err = runDoctorReport(t)
	require.NoError(t, err)
	pass, warn, detail, _ = doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, pass, detail)
	require.False(t, warn, "the printed fix restores every trigger: %s", detail)
	var enabled string
	require.NoError(t, pool.QueryRow(ctx, `SELECT tgenabled::text FROM pg_trigger
		WHERE tgrelid = 'sync_events'::regclass AND tgname = 'sync_events_no_truncate'`).Scan(&enabled))
	require.Equal(t, "A", enabled, "the ALWAYS guard is left as it was")
}

// TestDoctorHubTriggers_TriggerBoundToAnotherFunction_ReportedWithFix: a
// trigger with the expected name that executes another function than its
// migration binds, a row trigger or a TRUNCATE guard, is reported, and the
// fix is mtix sync init alone, run as the table owner: init replaces both
// inside its one transaction. Afterwards each executes its own function,
// and a guard that was already in place is left as it was (MTIX-95.7).
func TestDoctorHubTriggers_TriggerBoundToAnotherFunction_ReportedWithFix(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	pool := hubPool(t, dsn)
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DROP FUNCTION IF EXISTS mtix_test_noop_trigger() CASCADE`,
			`DROP SCHEMA IF EXISTS mtix_elsewhere CASCADE`,
		} {
			_, err := pool.Exec(context.Background(), stmt)
			require.NoError(t, err)
		}
	})
	guardOID := func(table, name string) (oid uint32, function string) {
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT t.oid, n.nspname::text || '.' || p.proname::text FROM pg_catalog.pg_trigger t
			JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
			JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
			WHERE t.tgrelid = pg_catalog.to_regclass($1) AND t.tgname = $2`, table, name).Scan(&oid, &function))
		return oid, function
	}
	untouched, _ := guardOID("sync_conflicts", "sync_conflicts_no_truncate")
	for _, stmt := range []string{
		`CREATE OR REPLACE FUNCTION mtix_test_noop_trigger() RETURNS trigger AS $$ BEGIN RETURN NULL; END $$ LANGUAGE plpgsql`,
		`DROP TRIGGER audit_log_no_update ON audit_log`,
		`CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log FOR EACH ROW EXECUTE FUNCTION mtix_test_noop_trigger()`,
		`DROP TRIGGER sync_events_no_truncate ON sync_events`,
		`CREATE TRIGGER sync_events_no_truncate BEFORE TRUNCATE ON sync_events FOR EACH STATEMENT EXECUTE FUNCTION mtix_test_noop_trigger()`,
		// A no-op of the guard function's own name, in another schema.
		`CREATE SCHEMA mtix_elsewhere`,
		`CREATE FUNCTION mtix_elsewhere.append_only_no_truncate() RETURNS trigger AS $$ BEGIN RETURN NULL; END $$ LANGUAGE plpgsql`,
		`DROP TRIGGER audit_log_no_truncate ON audit_log`,
		`CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log FOR EACH STATEMENT EXECUTE FUNCTION mtix_elsewhere.append_only_no_truncate()`,
	} {
		_, err := pool.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	report, err := runDoctorReport(t)
	require.NoError(t, err, "a WARN keeps the doctor's exit code 0")
	pass, warn, detail, fix := doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, pass)
	require.True(t, warn)
	require.Contains(t, detail, "audit_log_no_update on public.audit_log calls public.mtix_test_noop_trigger, not public.audit_log_immutable")
	require.Contains(t, detail, "sync_events_no_truncate on public.sync_events calls public.mtix_test_noop_trigger, not public.append_only_no_truncate")
	require.Contains(t, detail, "audit_log_no_truncate on public.audit_log calls mtix_elsewhere.append_only_no_truncate, not public.append_only_no_truncate",
		"a function of the right name in another schema is another function")
	require.Equal(t, "as the table owner (postgres): mtix sync init", fix,
		"init alone replaces both triggers; no separate DROP step")

	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncInit(ctx, &stdout, &stderr, nil, cloudOpts), stderr.String())
	report, err = runDoctorReport(t)
	require.NoError(t, err)
	pass, warn, detail, _ = doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, pass, detail)
	require.False(t, warn, "mtix sync init alone restores the triggers: %s", detail)
	_, fn := guardOID("audit_log", "audit_log_no_update")
	require.Equal(t, "public.audit_log_immutable", fn)
	_, fn = guardOID("sync_events", "sync_events_no_truncate")
	require.Equal(t, "public.append_only_no_truncate", fn)
	_, fn = guardOID("audit_log", "audit_log_no_truncate")
	require.Equal(t, "public.append_only_no_truncate", fn, "init replaces the guard bound to the other schema's function")
	again, fn := guardOID("sync_conflicts", "sync_conflicts_no_truncate")
	require.Equal(t, "public.append_only_no_truncate", fn)
	require.Equal(t, untouched, again, "a guard already in place is not dropped and re-created")
}

// TestDoctorText_DisabledTrigger_ShowsFixWithOwnerAndStatement: without
// --json the doctor prints the hub-triggers fix under the check, naming
// the table owner and the exact ALTER TABLE statement (MTIX-95.7).
func TestDoctorText_DisabledTrigger_ShowsFixWithOwnerAndStatement(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	pool := hubPool(t, dsn)
	_, err := pool.Exec(context.Background(), `ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_update`)
	require.NoError(t, err)

	app.jsonOutput = false
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncDoctor(context.Background(), &stdout, &stderr, nil, cloudOpts), "a WARN exits 0")
	out := stdout.String()
	require.Contains(t, out, "[WARN] hub-triggers")
	require.Contains(t, out,
		"\n       fix: as the table owner (postgres): ALTER TABLE public.audit_log ENABLE TRIGGER audit_log_no_update;\n")
}
