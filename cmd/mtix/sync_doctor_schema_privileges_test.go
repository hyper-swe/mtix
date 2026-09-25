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
// runs (MTIX-95.1.7). Without a printable statement, or for a grant only
// mtix sync harden clears (another grantor, or a grant option), the fix is
// mtix sync harden, after any REVOKE statements.
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
		{"a grant only mtix sync harden clears", schemaState{projects: true, canRecord: true, hub: owner,
			collisionPrivileges: held[:1], collisionHarden: true},
			false, true, true, "as the table owner (mtix_owner): mtix sync harden"},
		{"the REVOKE statements, then mtix sync harden", schemaState{projects: true, canRecord: true, hub: owner,
			collisionPrivileges: held, collisionHarden: true,
			revokes: []string{"REVOKE USAGE ON SEQUENCE public.sync_node_collisions_collision_id_seq FROM syncer;"}},
			false, true, true, "as the table owner (mtix_owner): REVOKE USAGE ON SEQUENCE " +
				"public.sync_node_collisions_collision_id_seq FROM syncer;, then mtix sync harden"},
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
			require.Equal(t, tt.wantFix, got.Fix)
		})
	}
}
