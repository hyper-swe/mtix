// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGradeSchemaCurrent_RoleHoldsCollisionPrivileges_WarnsWithRevoke: a
// connecting role other than the table owner that can write collision
// rows gets a WARN by default and a FAIL in strict mode, naming each
// privilege (MTIX-95.1.7). The fix is the table owner's exact REVOKE
// statements, then the paths a role administrator removes, by name; with
// neither, the privilege itself is named, so the fix is never empty.
func TestGradeSchemaCurrent_RoleHoldsCollisionPrivileges_WarnsWithRevoke(t *testing.T) {
	const revokes = "REVOKE INSERT ON TABLE public.sync_node_collisions FROM syncer; " +
		"REVOKE USAGE ON SEQUENCE public.sync_node_collisions_collision_id_seq FROM syncer;"
	owner := hubObjectState{owners: []string{"mtix_owner"}, ownerIdents: []string{"mtix_owner"},
		tablesSchema: "public", tablesSchemaIdent: "public", currentSchema: "public"}
	held := []string{"INSERT on sync_node_collisions", "USAGE on sync_node_collisions_collision_id_seq"}
	const pathsIntro = `a role administrator removes each of these paths (see "Hub health checks" in the user manual): `
	tests := []struct {
		name     string
		state    schemaState
		strict   bool
		wantPass bool
		wantWarn bool
		wantFix  string
	}{
		{"warns with the REVOKE statements", schemaState{projects: true, canRecord: true, hub: owner,
			collisionPrivileges: held, revokes: []string{
				"REVOKE INSERT ON TABLE public.sync_node_collisions FROM syncer;",
				"REVOKE USAGE ON SEQUENCE public.sync_node_collisions_collision_id_seq FROM syncer;"}},
			false, true, true, "as the table owner (mtix_owner): " + revokes},
		{"fails in strict mode", schemaState{projects: true, canRecord: true, hub: owner,
			collisionPrivileges: held[:1], revokes: []string{"REVOKE INSERT ON TABLE public.sync_node_collisions FROM syncer;"}},
			true, false, false, "as the table owner (mtix_owner): REVOKE INSERT ON TABLE public.sync_node_collisions FROM syncer;"},
		{"no statement and no named path still names the privilege", schemaState{projects: true, canRecord: true, hub: owner,
			collisionPrivileges: held[1:]},
			false, true, true, pathsIntro + held[1] + " held through a grant or a role membership"},
		{"a named path for a role administrator", schemaState{projects: true, canRecord: true, hub: owner,
			collisionPrivileges: held[:1], collisionPaths: []string{"SET ROLE to w, which holds " + held[0]}},
			false, true, true, pathsIntro + "SET ROLE to w, which holds " + held[0]},
		{"the owner's REVOKE, then the named paths", schemaState{projects: true, canRecord: true, hub: owner,
			collisionPrivileges: held, collisionPaths: []string{held[0] + " inherited from pg_write_all_data"},
			revokes: []string{"REVOKE USAGE ON SEQUENCE public.sync_node_collisions_collision_id_seq FROM syncer;"}},
			false, true, true, "as the table owner (mtix_owner): REVOKE USAGE ON SEQUENCE " +
				"public.sync_node_collisions_collision_id_seq FROM syncer;, then " + pathsIntro +
				held[0] + " inherited from pg_write_all_data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gradeSchemaCurrent(tt.state, tt.strict)
			require.Equal(t, tt.wantPass, got.Pass, got.Detail)
			require.Equal(t, tt.wantWarn, got.Warn, got.Detail)
			for _, p := range tt.state.collisionPrivileges {
				require.Contains(t, got.Detail, p)
			}
			require.Contains(t, got.Detail, "once every syncing client is upgraded")
			if len(tt.state.collisionPaths) > 0 {
				require.Contains(t, got.Detail, "run each part of the fix as the role it names")
			}
			require.Equal(t, tt.wantFix, got.Fix)
		})
	}
}
