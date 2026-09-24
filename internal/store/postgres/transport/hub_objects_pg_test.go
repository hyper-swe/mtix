// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// TestMigrationObjects_MatchMigratedHub: the table, function and trigger
// sets parsed from the embedded migrations equal what a freshly migrated
// hub holds (MTIX-95.1). The fixture database holds nothing else, so any
// table the parser misses, or invents, shows up here.
func TestMigrationObjects_MatchMigratedHub(t *testing.T) {
	f := newHubFixture(t)
	owner := f.ownerRole()
	pool := f.openAs(owner, transport.Options{})
	require.NoError(t, pool.Migrate(context.Background()))

	tables, err := migrations.Tables()
	require.NoError(t, err)
	require.Equal(t, tables, f.queryStrings(
		`SELECT tablename::text FROM pg_catalog.pg_tables WHERE schemaname = 'public' ORDER BY 1`))

	functions, err := migrations.Functions()
	require.NoError(t, err)
	require.Equal(t, functions, f.queryStrings(
		`SELECT p.proname::text FROM pg_catalog.pg_proc p
		 JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
		 WHERE n.nspname = 'public' ORDER BY 1`))

	triggers, err := migrations.Triggers()
	require.NoError(t, err)
	want := make([]string, 0, len(triggers))
	for _, tr := range triggers {
		want = append(want, tr.Table+"."+tr.Name)
	}
	require.Equal(t, want, f.queryStrings(
		`SELECT c.relname || '.' || t.tgname FROM pg_catalog.pg_trigger t
		 JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
		 JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND NOT t.tgisinternal ORDER BY c.relname, t.tgname`))
}

// TestMigrateAndHarden_NoRowLevelSecurity: neither migrate nor harden
// enables or forces row-level security on any sync table (MTIX-95.1,
// F-03).
func TestMigrateAndHarden_NoRowLevelSecurity(t *testing.T) {
	f := newHubFixture(t)
	owner := f.ownerRole()
	warnings := transport.NewWarningLog()
	pool := f.openAs(owner, transport.Options{OnNotice: warnings.Record})
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))
	f.exec(`GRANT SELECT ON ALL TABLES IN SCHEMA public TO PUBLIC`)

	result, err := pool.Harden(ctx, transport.HardenRequest{Apply: true, Warnings: warnings})
	require.NoError(t, err)
	require.True(t, result.Applied, "harden had grants to revoke")
	require.NotEmpty(t, result.Executed)

	tables, err := migrations.Tables()
	require.NoError(t, err)
	var resolved int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT count(*) FROM unnest($1::text[]) AS t(name)
		 JOIN pg_catalog.pg_class c ON c.oid = pg_catalog.to_regclass(t.name)`,
		tables).Scan(&resolved))
	require.Equal(t, len(tables), resolved, "every sync table exists")
	require.Empty(t, f.queryStrings(
		`SELECT c.relname::text FROM unnest($1::text[]) AS t(name)
		 JOIN pg_catalog.pg_class c ON c.oid = pg_catalog.to_regclass(t.name)
		 WHERE c.relrowsecurity OR c.relforcerowsecurity`, tables),
		"no sync table has row-level security enabled or forced")
}

// TestHarden_WarningBeforeStatements_FailsRun: a WARNING that arrives
// before --apply runs its first statement (while the hub is read) fails
// the run and names the read, and nothing changes (MTIX-95.1).
func TestHarden_WarningBeforeStatements_FailsRun(t *testing.T) {
	f := newHubFixture(t)
	owner := f.ownerRole()
	warnings := transport.NewWarningLog()
	pool := f.openAs(owner, transport.Options{OnNotice: warnings.Record})
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))
	f.exec(`GRANT SELECT ON ALL TABLES IN SCHEMA public TO PUBLIC`)

	warnings.Record(&pgconn.Notice{SeverityUnlocalized: "WARNING", Message: "early warning"})
	_, err := pool.Harden(ctx, transport.HardenRequest{Apply: true, Warnings: warnings})
	require.ErrorIs(t, err, transport.ErrHubWarning)
	require.Contains(t, err.Error(), "reading the hub raised a server WARNING: early warning")
	require.Equal(t, []string{"true"}, f.queryStrings(
		`SELECT pg_catalog.has_table_privilege('public', 'public.audit_log', 'SELECT')::text`),
		"the grant to PUBLIC is still there")
}

// TestVerifyHubPrivileges_NonOwner_GetsReport: VerifyHubPrivileges, which
// sync doctor uses, reports to any calling role, while Harden refuses a
// non-owner; an invalid kept role is refused (MTIX-95.1).
func TestVerifyHubPrivileges_NonOwner_GetsReport(t *testing.T) {
	f := newHubFixture(t)
	owner := f.ownerRole()
	team := f.role("team")
	ownerPool := f.openAs(owner, transport.Options{})
	ctx := context.Background()
	require.NoError(t, ownerPool.Migrate(ctx))
	f.exec(`GRANT SELECT ON audit_log TO PUBLIC`)
	f.ddl(`GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA public TO %I`, team)

	pool := f.openAs(team, transport.Options{})
	report, err := pool.VerifyHubPrivileges(ctx, nil)
	require.NoError(t, err, "a non-owner gets a report")
	require.Equal(t, []string{owner}, report.Owners)
	require.False(t, report.Clean())
	var public bool
	for _, fd := range report.Findings {
		public = public || (fd.Role == "PUBLIC" && fd.Object == "public.audit_log")
	}
	require.True(t, public, "the grant to PUBLIC is reported: %+v", report.Findings)

	_, err = pool.Harden(ctx, transport.HardenRequest{})
	require.ErrorIs(t, err, transport.ErrHardenNotOwner, "harden still refuses a non-owner")
	_, err = pool.VerifyHubPrivileges(ctx, []string{"anon"})
	require.ErrorIs(t, err, model.ErrInvalidInput)
}
