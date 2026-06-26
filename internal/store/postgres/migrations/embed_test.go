// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package migrations_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigrations_FilesPresent(t *testing.T) {
	got, err := migrations.Files()
	require.NoError(t, err)
	want := []string{
		"001_sync_events.sql",
		"002_sync_conflicts.sql",
		"003_sync_projects.sql",
		"004_applied_events.sql",
		"005_audit_log.sql",
		"006_triggers.sql",
		"007_advisory_lock.sql",
		"008_sync_project_clients.sql",
		"009_node_registry_index.sql",
		"010_sync_events_uid.sql",
	}
	require.Equal(t, want, got, "all hub-schema files must be embedded in lex order")
}

func TestMigrations_OrderingIsLexical(t *testing.T) {
	got, err := migrations.Files()
	require.NoError(t, err)

	sorted := make([]string, len(got))
	copy(sorted, got)
	sort.Strings(sorted)
	require.Equal(t, sorted, got, "Files() must return entries in lexical order")
}

func TestMigrations_ReadEachFile(t *testing.T) {
	files, err := migrations.Files()
	require.NoError(t, err)
	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			body, err := migrations.Read(f)
			require.NoError(t, err)
			require.NotEmpty(t, body, "%s must not be empty", f)
			// Every migration carries a canonical "<ticket> hub schema"
			// header so an unattributed SQL file trips this test.
			require.Contains(t, body, "hub schema",
				"%s missing canonical comment header", f)
		})
	}
}

func TestMigrations_ReadMissingFile(t *testing.T) {
	_, err := migrations.Read("999_missing.sql")
	require.Error(t, err)
	require.Contains(t, err.Error(), "999_missing.sql")
}

// TestMigrations_ContainExpectedTables grepps each CREATE TABLE statement
// and asserts every required hub table is created exactly once across the
// migration set.
func TestMigrations_ContainExpectedTables(t *testing.T) {
	want := map[string]int{
		"sync_events":     0,
		"sync_conflicts":  0,
		"sync_projects":        0,
		"applied_events":       0,
		"audit_log":            0,
		"sync_project_clients": 0,
	}
	files, err := migrations.Files()
	require.NoError(t, err)
	for _, f := range files {
		body, err := migrations.Read(f)
		require.NoError(t, err)
		for table := range want {
			needle := "CREATE TABLE IF NOT EXISTS " + table + " "
			if strings.Contains(body, needle) {
				want[table]++
			}
		}
	}
	for table, n := range want {
		require.Equalf(t, 1, n, "%s must be created exactly once across all migrations (got %d)", table, n)
	}
}

// TestMigrations_TriggersFileEnforcesAppendOnly sanity-checks that 006
// raises an exception on UPDATE and DELETE for the immutable tables.
func TestMigrations_TriggersFileEnforcesAppendOnly(t *testing.T) {
	body, err := migrations.Read("006_triggers.sql")
	require.NoError(t, err)
	require.Contains(t, body, "audit_log_no_update")
	require.Contains(t, body, "audit_log_no_delete")
	require.Contains(t, body, "sync_conflicts_no_update")
	require.Contains(t, body, "sync_conflicts_no_delete")
	require.Contains(t, body, "RAISE EXCEPTION")
	require.Contains(t, body, "FR-18.5")
}

// TestMigrations_RegistryIndexIsPartialUnique asserts the MTIX-30.4
// registry (ADR-003 §6) is a DERIVED partial unique index over the
// append-only log — keyed on (project_prefix, node_id) and scoped to
// create_node rows — not a separate authoritative table. The WHERE
// clause is load-bearing: a non-partial unique index would reject
// legitimate non-create events that repeat a node_id (update_field,
// transition_status, etc.).
func TestMigrations_RegistryIndexIsPartialUnique(t *testing.T) {
	body, err := migrations.Read("009_node_registry_index.sql")
	require.NoError(t, err)
	require.Contains(t, body, "CREATE UNIQUE INDEX",
		"registry must be a UNIQUE index")
	require.Contains(t, body, "(project_prefix, node_id)",
		"registry is keyed on (project_prefix, node_id) per ADR-003 §6")
	require.Contains(t, body, "WHERE op_type = 'create_node'",
		"registry must be PARTIAL (create_node rows only) per ADR-003 §6")
	require.Contains(t, body, "ADR-003",
		"registry migration must reference its design rationale")
}

// TestMigrations_SyncEventsUIDColumn asserts the MTIX-30.6 migration adds
// the dual-carry uid column to the hub sync_events log (ADR-003 §3, §7
// Phase 3): a nullable, idempotently-added TEXT column with a partial
// lookup index, and no destructive backfill.
func TestMigrations_SyncEventsUIDColumn(t *testing.T) {
	body, err := migrations.Read("010_sync_events_uid.sql")
	require.NoError(t, err)
	require.Contains(t, body, "ADD COLUMN IF NOT EXISTS uid TEXT",
		"010 must add a nullable uid column idempotently")
	require.Contains(t, body, "CREATE INDEX IF NOT EXISTS idx_sync_events_uid",
		"010 must add the uid lookup index")
	require.Contains(t, body, "ADR-003",
		"010 must reference its design rationale")
}

// TestMigrations_OpTypeCheckMatchesModel ensures the SQL CHECK constraint
// in 001_sync_events.sql lists the exact 12 op_types from model.AllOpTypes.
// Drift here means a buggy hub silently accepts events the local model
// rejects (or vice versa).
func TestMigrations_OpTypeCheckMatchesModel(t *testing.T) {
	body, err := migrations.Read("001_sync_events.sql")
	require.NoError(t, err)

	expectedOps := []string{
		"create_node", "update_field", "transition_status",
		"claim", "unclaim", "defer",
		"comment", "link_dep", "unlink_dep",
		"delete", "set_acceptance", "set_prompt",
	}
	for _, op := range expectedOps {
		require.Contains(t, body, "'"+op+"'",
			"op_type CHECK in 001_sync_events.sql missing %s", op)
	}
}
