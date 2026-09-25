// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package migrations_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
)

// TestTables_EmbeddedMigrations_ReturnsEveryCreatedTable pins the sync
// table set that hub hardening and backup coverage share (MTIX-95.1). The
// list is derived from the embedded migrations, so a new CREATE TABLE
// reaches every consumer without a hand-written list.
func TestTables_EmbeddedMigrations_ReturnsEveryCreatedTable(t *testing.T) {
	got, err := migrations.Tables()
	require.NoError(t, err)
	require.Equal(t, []string{
		"applied_events",
		"audit_log",
		"node_renumber_remaps",
		"sync_conflicts",
		"sync_events",
		"sync_hub_state",
		"sync_node_collisions",
		"sync_project_clients",
		"sync_projects",
	}, got)
}

// TestSequences_EmbeddedMigrations_ReturnsEverySerialSequence pins the
// sequences the sync tables' serial columns create, which a backup role
// needs SELECT on (MTIX-95.1.4). A PG test pins them to a migrated hub.
func TestSequences_EmbeddedMigrations_ReturnsEverySerialSequence(t *testing.T) {
	got, err := migrations.Sequences()
	require.NoError(t, err)
	require.Equal(t, []string{
		"audit_log_audit_id_seq",
		"sync_conflicts_conflict_id_seq",
		"sync_node_collisions_collision_id_seq",
	}, got)
}

// TestMigrations_SequenceForms_AllParsed: every sequence a migration
// creates comes from a serial column of a CREATE TABLE, the only form
// Sequences() reads. A migration that creates one another way (an identity
// column, a serial column added by ALTER TABLE, CREATE SEQUENCE, or a
// quoted table or column name, which the parser does not read) fails here,
// so Sequences() never silently misses a sequence (MTIX-95.1.4).
func TestMigrations_SequenceForms_AllParsed(t *testing.T) {
	comment := regexp.MustCompile(`--[^\n]*`)
	unparsed := map[string]*regexp.Regexp{
		"an identity column":             regexp.MustCompile(`(?i)\bGENERATED\s+(?:ALWAYS|BY\s+DEFAULT)\s+AS\s+IDENTITY\b`),
		"a serial column added later":    regexp.MustCompile(`(?is)\bADD\s+(?:COLUMN\s+)?(?:IF\s+NOT\s+EXISTS\s+)?[a-z_][a-z0-9_]*\s+(?:smallserial|bigserial|serial[248]?)\b`),
		"an explicitly created sequence": regexp.MustCompile(`(?i)\bCREATE\s+(?:TEMP\s+|TEMPORARY\s+|UNLOGGED\s+)?SEQUENCE\b`),
		"a quoted serial column name":    regexp.MustCompile(`(?i)"[^"]+"\s+(?:smallserial|bigserial|serial[248]?)\b`),
		"a quoted table name":            regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"`),
	}
	files, err := migrations.Files()
	require.NoError(t, err)
	for _, f := range files {
		body, err := migrations.Read(f)
		require.NoError(t, err)
		sql := comment.ReplaceAllString(body, "")
		for form, re := range unparsed {
			require.Falsef(t, re.MatchString(sql), "%s creates a sequence with %s, which Sequences() does not read", f, form)
		}
	}
}

// TestFunctions_EmbeddedMigrations_ReturnsEveryFunction pins the mtix
// functions whose privileges hub hardening manages (MTIX-95.1).
func TestFunctions_EmbeddedMigrations_ReturnsEveryFunction(t *testing.T) {
	got, err := migrations.Functions()
	require.NoError(t, err)
	require.Equal(t, []string{"append_only_no_truncate", "audit_log_immutable"}, got)
}

// TestTriggers_EmbeddedMigrations_IncludesTruncateGuards pins every trigger
// the migrations create, including the TRUNCATE guards that migration 016
// creates inside a DO block (MTIX-95.1), and the function each executes
// (MTIX-95.7).
func TestTriggers_EmbeddedMigrations_IncludesTruncateGuards(t *testing.T) {
	got, err := migrations.Triggers()
	require.NoError(t, err)
	require.Equal(t, []migrations.Trigger{
		{Name: "audit_log_no_delete", Table: "audit_log", Event: "DELETE", Function: "audit_log_immutable"},
		{Name: "audit_log_no_truncate", Table: "audit_log", Event: "TRUNCATE", Function: "append_only_no_truncate"},
		{Name: "audit_log_no_update", Table: "audit_log", Event: "UPDATE", Function: "audit_log_immutable"},
		{Name: "sync_conflicts_no_delete", Table: "sync_conflicts", Event: "DELETE", Function: "audit_log_immutable"},
		{Name: "sync_conflicts_no_truncate", Table: "sync_conflicts", Event: "TRUNCATE", Function: "append_only_no_truncate"},
		{Name: "sync_conflicts_no_update", Table: "sync_conflicts", Event: "UPDATE", Function: "audit_log_immutable"},
		{Name: "sync_events_no_truncate", Table: "sync_events", Event: "TRUNCATE", Function: "append_only_no_truncate"},
	}, got)
}

// TestTruncateGuards_EmbeddedMigrations_CoverAppendOnlyTables pins the
// TRUNCATE guard set: audit_log, sync_conflicts and sync_events each carry
// one, all calling append_only_no_truncate (MTIX-95.1).
func TestTruncateGuards_EmbeddedMigrations_CoverAppendOnlyTables(t *testing.T) {
	got, err := migrations.TruncateGuards()
	require.NoError(t, err)
	tables := make([]string, 0, len(got))
	for _, g := range got {
		require.Equal(t, "TRUNCATE", g.Event)
		tables = append(tables, g.Table)
	}
	require.Equal(t, []string{"audit_log", "sync_conflicts", "sync_events"}, tables)
}

// TestTruncateGuardMigration_CreatesOnlyWhenAbsent pins the shape of 016:
// a guard is created only when pg_trigger lacks a trigger of its name that
// executes this migration's append_only_no_truncate, compared by OID, so a
// re-run on a hub whose guards are in place takes no table lock for them
// (MTIX-95.1). In that same branch a trigger of the guard's name bound to
// another function, one of the same name in another schema included, is
// dropped first, so mtix sync init replaces it inside its one transaction
// (MTIX-95.7).
func TestTruncateGuardMigration_CreatesOnlyWhenAbsent(t *testing.T) {
	body, err := migrations.Read(migrations.TruncateGuardFile)
	require.NoError(t, err)
	require.Contains(t, body, "CREATE OR REPLACE FUNCTION append_only_no_truncate()")
	require.Contains(t, body, "FOR EACH STATEMENT")
	require.Equal(t, 3, strings.Count(body, "IF NOT EXISTS (SELECT 1 FROM pg_trigger"),
		"each guard is created only when absent")
	require.Contains(t, body, "guard_fn oid := pg_catalog.to_regprocedure(\n"+
		"        pg_catalog.quote_ident(pg_catalog.current_schema()) || '.append_only_no_truncate()');",
		"the guard function is resolved once, schema-qualified, to its OID")
	require.Equal(t, 3, strings.Count(body, "AND t.tgfoid = guard_fn"),
		"a guard counts as present only when it executes that function, by OID")
	require.NotContains(t, body, "proname", "no guard is matched by function name")
	guards, err := migrations.TruncateGuards()
	require.NoError(t, err)
	for _, g := range guards {
		ifAt := strings.Index(body, "t.tgname = '"+g.Name+"'")
		dropAt := strings.Index(body, "DROP TRIGGER IF EXISTS "+g.Name+" ON "+g.Table+";")
		createAt := strings.Index(body, "CREATE TRIGGER "+g.Name)
		require.Truef(t, ifAt >= 0 && ifAt < dropAt && dropAt < createAt,
			"%s: the drop sits inside the guard's IF block, before its CREATE", g.Name)
	}
	require.Equal(t, 3, strings.Count(body, "DROP TRIGGER"), "nothing else is dropped")
}

// TestTruncateGuardMigration_RefusesWhenTheTablesAreInAnotherSchema pins
// that 016 first checks that the schema of every guarded table is
// current_schema(), where it creates the guard function, and raises
// otherwise, before it creates or changes anything (MTIX-95.7).
func TestTruncateGuardMigration_RefusesWhenTheTablesAreInAnotherSchema(t *testing.T) {
	body, err := migrations.Read(migrations.TruncateGuardFile)
	require.NoError(t, err)
	check := strings.Index(body, "RAISE EXCEPTION 'the sync tables are in schema %, but the first schema on the search_path is %")
	create := strings.Index(body, "CREATE OR REPLACE FUNCTION append_only_no_truncate()")
	require.True(t, check >= 0 && check < create, "the schema check comes before anything is created")
	for _, tbl := range []string{"audit_log", "sync_conflicts", "sync_events"} {
		require.Contains(t, body[:create], "'"+tbl+"'", "the check covers %s", tbl)
	}
}

// TestMigrations_NoRowLevelSecurity keeps row-level security out of the
// 0.5.x hub schema: no migration enables or forces it (MTIX-95.1).
func TestMigrations_NoRowLevelSecurity(t *testing.T) {
	files, err := migrations.Files()
	require.NoError(t, err)
	for _, f := range files {
		body, err := migrations.Read(f)
		require.NoError(t, err)
		require.NotContains(t, strings.ToUpper(body), "ROW LEVEL SECURITY", f)
	}
}

// TestFunctions_EmbeddedMigrations_TakeNoArguments pins an assumption hub
// hardening relies on: it names every mtix function as name(), so each one
// the migrations define must take no arguments (MTIX-95.1). A migration
// that adds a function with arguments fails here until hardening learns
// its signature.
func TestFunctions_EmbeddedMigrations_TakeNoArguments(t *testing.T) {
	files, err := migrations.Files()
	require.NoError(t, err)
	fns, err := migrations.Functions()
	require.NoError(t, err)
	for _, fn := range fns {
		found := false
		for _, f := range files {
			body, err := migrations.Read(f)
			require.NoError(t, err)
			if strings.Contains(body, "FUNCTION "+fn+"()") {
				found = true
			}
		}
		require.Truef(t, found, "%s must be defined as %s()", fn, fn)
	}
}
