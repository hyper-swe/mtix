// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHarden_GuardBoundToAnotherFunction_ListedAndReplacedByApply: a
// TRUNCATE guard that executes another function, here a no-op of the
// guard function's own name in another schema, is listed by the dry run
// as a change the guard migration makes, and --apply replaces it, so the
// guard executes the migration's own function again (MTIX-95.7).
func TestHarden_GuardBoundToAnotherFunction_ListedAndReplacedByApply(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner := f.ownerRole()
	f.migrateAs(owner)
	f.exec(`CREATE SCHEMA mtix_elsewhere`)
	f.exec(`CREATE FUNCTION mtix_elsewhere.append_only_no_truncate() RETURNS trigger AS $$ BEGIN RETURN NULL; END $$ LANGUAGE plpgsql`)
	f.exec(`DROP TRIGGER sync_events_no_truncate ON sync_events`)
	f.exec(`CREATE TRIGGER sync_events_no_truncate BEFORE TRUNCATE ON sync_events ` +
		`FOR EACH STATEMENT EXECUTE FUNCTION mtix_elsewhere.append_only_no_truncate()`)
	bound := `SELECT n.nspname || '.' || p.proname
		FROM pg_catalog.pg_trigger t
		JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
		JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
		WHERE t.tgrelid = 'sync_events'::regclass AND t.tgname = 'sync_events_no_truncate'`
	require.Equal(t, []string{"mtix_elsewhere.append_only_no_truncate"}, f.strings(bound))

	out, err := f.harden(owner)
	require.Equal(t, 2, exitCodeForError(err), "the dry run has a change to make: %s", out)
	require.Contains(t, out, "sync_events_no_truncate on public.sync_events")
	require.Contains(t, out, "run migration 016_append_only_truncate_guard.sql "+
		"(creates each missing guard and replaces one that executes another function)")
	require.NotContains(t, out, "an administrator must act", "the migration replaces the guard; no administrator is needed")

	out, err = f.harden(owner, "--apply")
	require.NoError(t, err, "the guard is replaced: %s", out)
	require.Equal(t, []string{"public.append_only_no_truncate"}, f.strings(bound))
}
