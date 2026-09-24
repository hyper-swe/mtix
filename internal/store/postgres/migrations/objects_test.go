// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package migrations_test

import (
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

// TestFunctions_EmbeddedMigrations_ReturnsEveryFunction pins the mtix
// functions whose privileges hub hardening manages (MTIX-95.1).
func TestFunctions_EmbeddedMigrations_ReturnsEveryFunction(t *testing.T) {
	got, err := migrations.Functions()
	require.NoError(t, err)
	require.Equal(t, []string{"append_only_no_truncate", "audit_log_immutable"}, got)
}

// TestTriggers_EmbeddedMigrations_IncludesTruncateGuards pins every trigger
// the migrations create, including the TRUNCATE guards that migration 016
// creates inside a DO block (MTIX-95.1).
func TestTriggers_EmbeddedMigrations_IncludesTruncateGuards(t *testing.T) {
	got, err := migrations.Triggers()
	require.NoError(t, err)
	require.Equal(t, []migrations.Trigger{
		{Name: "audit_log_no_delete", Table: "audit_log", Event: "DELETE"},
		{Name: "audit_log_no_truncate", Table: "audit_log", Event: "TRUNCATE"},
		{Name: "audit_log_no_update", Table: "audit_log", Event: "UPDATE"},
		{Name: "sync_conflicts_no_delete", Table: "sync_conflicts", Event: "DELETE"},
		{Name: "sync_conflicts_no_truncate", Table: "sync_conflicts", Event: "TRUNCATE"},
		{Name: "sync_conflicts_no_update", Table: "sync_conflicts", Event: "UPDATE"},
		{Name: "sync_events_no_truncate", Table: "sync_events", Event: "TRUNCATE"},
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
// the guards are created only when pg_trigger lacks them, never dropped and
// re-created, so a re-run takes no table lock for them (MTIX-95.1).
func TestTruncateGuardMigration_CreatesOnlyWhenAbsent(t *testing.T) {
	body, err := migrations.Read(migrations.TruncateGuardFile)
	require.NoError(t, err)
	require.Contains(t, body, "CREATE OR REPLACE FUNCTION append_only_no_truncate()")
	require.Contains(t, body, "FOR EACH STATEMENT")
	require.Equal(t, 3, strings.Count(body, "IF NOT EXISTS (SELECT 1 FROM pg_trigger"),
		"each guard is created only when absent")
	require.NotContains(t, body, "DROP TRIGGER", "a guard is never dropped and re-created")
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
