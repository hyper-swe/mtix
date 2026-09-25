// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// searchPathDSN returns the fixture database's superuser DSN with the
// given search_path.
func searchPathDSN(f *hardenFixture, searchPath string) string {
	u := f.dbURL
	q := u.Query()
	q.Set("options", "-c search_path="+searchPath)
	u.RawQuery = q.Encode()
	return u.String()
}

// hardenWithDSN runs mtix sync harden (with --insecure-tls and args)
// against dsn and returns its output and error.
func hardenWithDSN(t *testing.T, dsn string, args ...string) (string, error) {
	t.Helper()
	t.Setenv(transport.EnvDSN, dsn)
	cmd := newSyncCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"harden", "--insecure-tls"}, args...))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := cmd.ExecuteContext(ctx)
	return stdout.String() + stderr.String(), err
}

// searchPathHub is a hub whose sync tables are in schema mtix_hub, with a
// second, empty schema mtix_other.
type searchPathHub struct {
	f                 *hardenFixture
	hubDSN, otherDSN  string // search_path mtix_hub; mtix_other, mtix_hub
	intactGuardOIDs   []string
	objectsInOtherSQL string
}

// newSearchPathHub migrates a hub into schema mtix_hub through mtix sync
// init, with search_path set to that schema alone.
func newSearchPathHub(t *testing.T) *searchPathHub {
	t.Helper()
	initTestApp(t)
	f := newHardenFixture(t)
	f.exec(`CREATE SCHEMA mtix_hub`)
	f.exec(`CREATE SCHEMA mtix_other`)
	h := &searchPathHub{f: f, hubDSN: searchPathDSN(f, "mtix_hub"), otherDSN: searchPathDSN(f, "mtix_other,mtix_hub"),
		objectsInOtherSQL: `SELECT c.relname::text FROM pg_catalog.pg_class c
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'mtix_other'
			UNION ALL SELECT p.proname::text FROM pg_catalog.pg_proc p
			JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'mtix_other'`}
	t.Setenv(transport.EnvDSN, h.hubDSN)
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncInit(context.Background(), &stdout, &stderr, nil, cloudOpts), stderr.String())
	tables, err := migrations.Tables()
	require.NoError(t, err)
	require.Equal(t, tables, f.strings(`SELECT c.relname::text FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'mtix_hub' AND c.relkind = 'r' ORDER BY 1`), "the hub lives in mtix_hub")
	return h
}

// TestDoctor_TablesInNonPublicSchema_SchemaCurrentAndHubTriggersPass: a hub
// whose sync tables live in a schema other than public, reached with that
// schema first on the search_path, passes schema current, and hub-triggers
// then runs and passes (MTIX-95.7).
func TestDoctor_TablesInNonPublicSchema_SchemaCurrentAndHubTriggersPass(t *testing.T) {
	h := newSearchPathHub(t)
	t.Setenv(transport.EnvDSN, h.hubDSN)
	report, err := runDoctorReport(t)
	require.NoError(t, err)
	pass, _, detail, _ := doctorCheckNamed(t, report, "schema current")
	require.True(t, pass, detail)
	pass, warn, detail, _ := doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, pass, detail)
	require.False(t, warn, detail)
	require.Contains(t, detail, "present and enabled")
	require.NotContains(t, detail, "first schema on the search_path", "the tables' schema is first: no note")
}

// TestSearchPath_FirstSchemaIsNotTheTablesSchema_InitAndHardenRefuse: with
// the sync tables in mtix_hub and the search_path 'mtix_other, mtix_hub',
// anything mtix created would land in mtix_other, away from the tables it
// guards. mtix sync init, mtix sync harden --apply and the guard migration
// itself therefore refuse, naming both schemas, and change nothing: no
// object appears in mtix_other and the intact guards keep their OID. The
// harden dry run still reports, and it and the doctor agree on the one
// missing guard, even with a function of the guard function's name first
// on the search_path (MTIX-95.7).
func TestSearchPath_FirstSchemaIsNotTheTablesSchema_InitAndHardenRefuse(t *testing.T) {
	h := newSearchPathHub(t)
	f := h.f
	f.exec(`DROP TRIGGER audit_log_no_truncate ON mtix_hub.audit_log`)
	guardOIDs := `SELECT t.tgname::text || ' ' || t.oid::text FROM pg_catalog.pg_trigger t
		WHERE t.tgname LIKE '%\_no\_truncate' ORDER BY 1`
	before := f.strings(guardOIDs)
	require.Len(t, before, 2, "two intact guards")
	const refusal = "the sync tables are in schema mtix_hub, but the first schema on the search_path is mtix_other"
	requireUnchanged := func(step string) {
		t.Helper()
		require.Empty(t, f.strings(h.objectsInOtherSQL), "%s creates nothing in mtix_other", step)
		require.Equal(t, before, f.strings(guardOIDs), "%s leaves the intact guards as they were", step)
	}

	t.Setenv(transport.EnvDSN, h.otherDSN)
	var stdout, stderr bytes.Buffer
	err := runSyncInit(context.Background(), &stdout, &stderr, nil, cloudOpts)
	require.Error(t, err, "mtix sync init refuses")
	require.Contains(t, err.Error(), refusal)
	requireUnchanged("mtix sync init")

	out, err := hardenWithDSN(t, h.otherDSN, "--apply")
	require.Error(t, err, "mtix sync harden --apply refuses: %s", out)
	require.ErrorIs(t, err, transport.ErrSearchPathSchema)
	require.Contains(t, err.Error(), refusal)
	requireUnchanged("mtix sync harden --apply")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := transport.New(ctx, h.otherDSN, cloudOpts)
	require.NoError(t, err)
	defer pool.Close()
	body, err := migrations.Read(migrations.TruncateGuardFile)
	require.NoError(t, err)
	_, err = pool.Inner().Exec(ctx, body)
	require.Error(t, err, "the guard migration refuses on its own")
	require.Contains(t, err.Error(), refusal)
	requireUnchanged("the guard migration")

	// A function of the guard function's name first on the search_path does
	// not make the intact guards look rebound: doctor and harden compare
	// each guard with the function in the tables' schema.
	f.exec(`CREATE FUNCTION mtix_other.append_only_no_truncate() RETURNS trigger AS $$ BEGIN RETURN NULL; END $$ LANGUAGE plpgsql`)

	out, err = hardenWithDSN(t, h.otherDSN)
	require.Equal(t, 2, exitCodeForError(err), "the dry run still reports: %s", out)
	require.Contains(t, out, "audit_log_no_truncate on mtix_hub.audit_log")
	require.NotContains(t, out, "sync_events_no_truncate on", "the intact guards are not findings")

	t.Setenv(transport.EnvDSN, h.otherDSN)
	report, err := runDoctorReport(t)
	require.NoError(t, err)
	_, warn, detail, _ := doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, warn)
	require.Contains(t, detail, "missing triggers: audit_log_no_truncate on mtix_hub.audit_log")
	require.Contains(t, detail, "the first schema on the search_path is mtix_other, not mtix_hub")
	_, _, _, fix := doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, strings.HasPrefix(fix, "as the table owner ("+f.superuser+"): set the search_path so that mtix_hub comes first "+
		"(ALTER ROLE "+f.superuser+" SET search_path = mtix_hub, public), then mtix sync init"), fix)
	require.False(t, strings.Contains(detail, "sync_events_no_truncate") || strings.Contains(detail, "sync_conflicts_no_truncate"),
		"doctor agrees with harden: only the missing guard: %s", detail)
}

// TestHarden_SchemaNamedAfterRoleFirst_OnlyTheGuardMigrationIsRefused: the
// sync tables are in public and the owner has a schema of its own name,
// which the default search_path ("$user", public) puts first. --apply on a
// clean hub exits 0, and a privilege-only --apply revokes and creates
// nothing: neither runs the guard migration. Only an --apply that would run
// it (a missing guard) refuses, naming public as the schema to put first,
// and the doctor's fix starts with that step (MTIX-95.7).
func TestHarden_SchemaNamedAfterRoleFirst_OnlyTheGuardMigrationIsRefused(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner, bob := f.ownerRole(), f.role("bob")
	f.migrateAs(owner)
	f.ddl("CREATE SCHEMA %I AUTHORIZATION %I", owner, owner)
	inOwnSchema := `SELECT c.relname::text FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1
		UNION ALL SELECT p.proname::text FROM pg_catalog.pg_proc p
		JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = $1`

	out, err := f.harden(owner, "--apply")
	require.NoError(t, err, "a clean hub exits 0: %s", out)

	f.ddl("GRANT SELECT, INSERT ON public.sync_events TO %I", bob)
	out, err = f.harden(owner, "--apply")
	require.NoError(t, err, "a privilege-only --apply proceeds: %s", out)
	require.Equal(t, []string{"false"},
		f.strings(`SELECT pg_catalog.has_table_privilege($1, 'public.sync_events', 'SELECT')::text`, bob))
	require.Empty(t, f.strings(inOwnSchema, owner), "the privilege-only --apply creates nothing")

	f.exec(`DROP TRIGGER audit_log_no_truncate ON public.audit_log`)
	out, err = f.harden(owner, "--apply")
	require.ErrorIs(t, err, transport.ErrSearchPathSchema, out)
	require.Contains(t, err.Error(), "the sync tables are in schema public, but the first schema on the search_path is "+owner)
	require.Contains(t, err.Error(), `"ALTER ROLE <owner> SET search_path = public"`, "public alone, not public, public")
	require.Empty(t, f.strings(inOwnSchema, owner), "the refused --apply creates nothing")

	t.Setenv(transport.EnvDSN, f.dsnAs(owner))
	report, err := runDoctorReport(t)
	require.NoError(t, err)
	_, warn, detail, fix := doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, warn)
	require.Contains(t, detail, "the first schema on the search_path is "+owner+", not public")
	require.Equal(t, "as the table owner ("+owner+"): set the search_path so that public comes first "+
		"(ALTER ROLE "+owner+" SET search_path = public), then mtix sync init", fix)
}

// TestInit_UnrelatedTableOfASyncTableNameLaterOnPath_CreatesTheHubFirst: an
// application's own objects later on the search_path 'mtix, public' are
// not an mtix hub, even when they share sync table or function names: an
// mtix hub has sync_projects and sync_events in one schema together with
// an mtix function. Init creates every sync table in mtix and leaves the
// application's objects alone (MTIX-95.7).
func TestInit_UnrelatedTableOfASyncTableNameLaterOnPath_CreatesTheHubFirst(t *testing.T) {
	tests := []struct {
		name string
		app  []string
	}{
		{"an application's audit_log", []string{`CREATE TABLE public.audit_log (id integer PRIMARY KEY, note text)`}},
		{"sync_projects and sync_events without an mtix function", []string{
			`CREATE TABLE public.sync_projects (id integer PRIMARY KEY, note text)`,
			`CREATE TABLE public.sync_events (id integer PRIMARY KEY, note text)`}},
		{"sync_projects and an mtix-named function without sync_events", []string{
			`CREATE TABLE public.sync_projects (id integer PRIMARY KEY, note text)`,
			`CREATE FUNCTION public.audit_log_immutable() RETURNS trigger AS $$ BEGIN RETURN NULL; END $$ LANGUAGE plpgsql`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			f := newHardenFixture(t)
			f.exec(`CREATE SCHEMA mtix`)
			for _, stmt := range tt.app {
				f.exec(stmt)
			}
			appObjects := `SELECT c.relname::text FROM pg_catalog.pg_class c
				JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = 'public' AND c.relkind = 'r' ORDER BY 1`
			before := f.strings(appObjects)
			t.Setenv(transport.EnvDSN, searchPathDSN(f, "mtix,public"))
			var stdout, stderr bytes.Buffer
			require.NoError(t, runSyncInit(context.Background(), &stdout, &stderr, nil, cloudOpts), stderr.String())

			tables, err := migrations.Tables()
			require.NoError(t, err)
			require.Equal(t, tables, f.strings(`SELECT c.relname::text FROM pg_catalog.pg_class c
				JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = 'mtix' AND c.relkind = 'r' ORDER BY 1`), "every sync table is in mtix")
			require.Equal(t, before, f.strings(appObjects), "the application's tables are untouched")
		})
	}
}
