// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGradeSchemaCurrent_StateAndMode_ReportsEachGapWithItsFix grades the
// doctor's schema current check (MTIX-95.1.7): a hub without sync_projects
// fails as before; a hub with every object of migration 017 and a role that
// can execute the collision recorder passes; a hub that lacks an object of
// 017, or a role that cannot execute the recorder, is a WARN by default and
// a FAIL in strict mode, naming each gap and the exact fix, run as the
// table owner: mtix sync init (after the search_path step when init would
// refuse), and the GRANT the check printed. Pushes keep working on a hub
// without 017; for a role that cannot execute the recorder, a push that
// meets a restore collision fails until the owner runs the GRANT.
func TestGradeSchemaCurrent_StateAndMode_ReportsEachGapWithItsFix(t *testing.T) {
	const grant = "GRANT EXECUTE ON FUNCTION public.record_restore_collision(text, text, text, text, bigint) TO syncer;"
	owner := hubObjectState{owners: []string{"mtix_owner"}, ownerIdents: []string{"mtix_owner"},
		tablesSchema: "public", tablesSchemaIdent: "public", currentSchema: "public"}
	elsewhere := owner
	elsewhere.tablesSchema, elsewhere.tablesSchemaIdent, elsewhere.currentSchema = "hub", "hub", "scratch"
	missing := []string{"function record_restore_collision(text, text, text, text, bigint)",
		"trigger sync_events_stamp_restore_epoch on public.sync_events"}
	tests := []struct {
		name       string
		state      schemaState
		strict     bool
		wantPass   bool
		wantWarn   bool
		wantDetail []string
		notDetail  []string
		wantFix    string
	}{
		{name: "no sync_projects fails as before", state: schemaState{hub: owner},
			wantDetail: []string{"sync_projects table missing \u2014 run 'mtix sync init'"}},
		{name: "current hub passes", state: schemaState{projects: true, canRecord: true, hub: owner},
			wantPass: true, wantDetail: []string{"ok"}},
		{name: "missing 017 objects warn", state: schemaState{projects: true, canRecord: true, missing: missing, hub: owner},
			wantPass: true, wantWarn: true,
			wantDetail: []string{"migration 017", missing[0], missing[1], "pushes keep working"},
			wantFix:    "as the table owner (mtix_owner): mtix sync init"},
		{name: "missing 017 objects fail in strict mode", state: schemaState{projects: true, canRecord: true, missing: missing, hub: owner},
			strict: true, wantDetail: []string{"strict mode (sync.keep_roles is set)", missing[0]},
			wantFix: "as the table owner (mtix_owner): mtix sync init"},
		{name: "init after the search_path step", state: schemaState{projects: true, canRecord: true, missing: missing, hub: elsewhere},
			wantPass: true, wantWarn: true, wantDetail: []string{"migration 017"},
			wantFix: "as the table owner (mtix_owner): set the search_path so that hub comes first " +
				"(ALTER ROLE mtix_owner SET search_path = hub, public), then mtix sync init"},
		{name: "a role without EXECUTE warns with the grant", state: schemaState{projects: true, grant: grant, hub: owner},
			wantPass: true, wantWarn: true, wantDetail: []string{"cannot execute record_restore_collision",
				"a push that meets a restore collision fails until the table owner runs the printed GRANT"},
			notDetail: []string{"pushes keep working"},
			wantFix:   "as the table owner (mtix_owner): " + grant},
		{name: "both gaps, init first", state: schemaState{projects: true, grant: grant, hub: owner,
			missing: []string{"trigger sync_events_stamp_restore_epoch on public.sync_events"}},
			wantPass: true, wantWarn: true, wantDetail: []string{"migration 017", "cannot execute record_restore_collision"},
			notDetail: []string{"pushes keep working"},
			wantFix:   "as the table owner (mtix_owner): mtix sync init, then " + grant},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gradeSchemaCurrent(tt.state, tt.strict)
			require.Equal(t, "schema current", got.Name)
			require.Equal(t, tt.wantPass, got.Pass, got.Detail)
			require.Equal(t, tt.wantWarn, got.Warn, got.Detail)
			for _, d := range tt.wantDetail {
				require.Contains(t, got.Detail, d)
			}
			for _, d := range tt.notDetail {
				require.NotContains(t, got.Detail, d)
			}
			require.Equal(t, tt.wantFix, got.Fix)
		})
	}
}

// TestSyncDoctorCmd_CLIReference_MatchesHelp: docs/CLI_REFERENCE.md
// carries the doctor command's help exactly as the command prints it,
// schema current's migration 017 check included (MTIX-95.1.7).
func TestSyncDoctorCmd_CLIReference_MatchesHelp(t *testing.T) {
	cmd := newSyncDoctorCmd()
	long := strings.Join(strings.Fields(cmd.Long), " ")
	for _, want := range []string{"migration 017",
		"A hub without migration 017 (its owner has not run mtix sync init since the upgrade) is a WARN by default " +
			"and fails in strict mode; pushes keep working.",
		"A connecting role without EXECUTE on record_restore_collision is a WARN by default and fails in strict " +
			"mode; a push that meets a restore collision fails until the table owner runs the printed GRANT.",
		"INSERT on sync_node_collisions or USAGE on sync_node_collisions_collision_id_seq",
		"The check skips the table owner, roles that inherit it, and superusers."} {
		require.Contains(t, long, want)
	}
	ref, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.NoError(t, err)
	section := "## doctor\n\n**Usage:** `doctor`\n\n" + cmd.Short + "\n\n" + cmd.Long + "\n"
	require.Contains(t, string(ref), section, "docs/CLI_REFERENCE.md matches mtix sync doctor --help")
}
