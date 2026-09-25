// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGradeSchemaCurrent_RoleHoldsCollisionPrivileges_WarnsWithRevoke: a
// connecting role other than the table owner that holds INSERT on
// sync_node_collisions or USAGE on its sequence, which the least-privilege
// list does not name, gets a WARN by default and a FAIL in strict mode,
// naming each privilege, with the exact REVOKE statements the table owner
// runs (MTIX-95.1.7), and the membership REVOKE statements a role
// administrator runs; without any printable statement the fix is mtix sync
// harden, whose dry run names how the role holds the privilege, so the fix
// is never empty.
func TestGradeSchemaCurrent_RoleHoldsCollisionPrivileges_WarnsWithRevoke(t *testing.T) {
	const revokes = "REVOKE INSERT ON TABLE public.sync_node_collisions FROM syncer; " +
		"REVOKE USAGE ON SEQUENCE public.sync_node_collisions_collision_id_seq FROM syncer;"
	owner := hubObjectState{owners: []string{"mtix_owner"}, ownerIdents: []string{"mtix_owner"},
		tablesSchema: "public", tablesSchemaIdent: "public", currentSchema: "public"}
	held := []string{"INSERT on sync_node_collisions", "USAGE on sync_node_collisions_collision_id_seq"}
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
		{"no printable statement points to mtix sync harden", schemaState{projects: true, canRecord: true, hub: owner,
			collisionPrivileges: held[1:]},
			false, true, true, "as the table owner (mtix_owner): mtix sync harden"},
		{"a membership a role administrator revokes", schemaState{projects: true, canRecord: true, hub: owner,
			collisionPrivileges: held[:1], memberships: []string{"REVOKE w FROM syncer GRANTED BY postgres;"}},
			false, true, true, "as a role administrator: REVOKE w FROM syncer GRANTED BY postgres;"},
		{"the owner's REVOKE, then the membership REVOKE", schemaState{projects: true, canRecord: true, hub: owner,
			collisionPrivileges: held, memberships: []string{"REVOKE w FROM syncer GRANTED BY postgres;"},
			revokes: []string{"REVOKE USAGE ON SEQUENCE public.sync_node_collisions_collision_id_seq FROM syncer;"}},
			false, true, true, "as the table owner (mtix_owner): REVOKE USAGE ON SEQUENCE " +
				"public.sync_node_collisions_collision_id_seq FROM syncer;, then as a role administrator: " +
				"REVOKE w FROM syncer GRANTED BY postgres;"},
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
			if len(tt.state.memberships) > 0 {
				require.Contains(t, got.Detail, "run each part of the fix as the role it names")
			}
			require.Equal(t, tt.wantFix, got.Fix)
		})
	}
}
