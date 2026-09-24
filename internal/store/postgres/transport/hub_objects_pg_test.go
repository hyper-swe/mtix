// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

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
