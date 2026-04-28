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
	}
	require.Equal(t, want, got, "all 7 hub-schema files must be embedded in lex order")
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
			require.Contains(t, body, "MTIX-15.2 hub schema",
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
		"sync_projects":   0,
		"applied_events":  0,
		"audit_log":       0,
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
