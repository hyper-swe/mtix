// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// schemaHub is a hub migrated by owner into schema, which the fixture
// database puts first on every session's search_path, with a login role,
// syncer, holding the unscoped documented least-privilege list
// (MTIX-95.1.7).
type schemaHub struct {
	f                     *hardenFixture
	schema, owner, syncer string
	syncDSN               string
}

// newSchemaHub builds a schemaHub. Every identifier is quoted server-side,
// so schema may be any name.
func newSchemaHub(t *testing.T, schema string) *schemaHub {
	t.Helper()
	f := newHardenFixture(t)
	h := &schemaHub{f: f, schema: schema, owner: f.role("owner"), syncer: f.role("syncer")}
	f.ddl("CREATE SCHEMA %I AUTHORIZATION %I", schema, h.owner)
	f.ddl("ALTER DATABASE %I SET search_path = %I", f.dbName, schema)
	f.migrateAs(h.owner)
	for _, g := range expectedSyncGrants() {
		if g.scope == "" {
			f.ddl(grantTemplates[[2]string{g.privilege, g.kind}], g.object, h.syncer, schema)
		}
	}
	h.syncDSN = loginRoleDSN(t, f, h.syncer)
	return h
}

// ident returns name quoted as an SQL identifier, by the server.
func (h *schemaHub) ident(name string) string {
	h.f.t.Helper()
	return h.f.strings(`SELECT pg_catalog.format('%I', $1::text)`, name)[0]
}

// requireSchemaCurrentFix asserts that the schema current check of a
// doctor run with the syncing role's DSN warns, names each of held, and
// prints fix as the table owner.
func (h *schemaHub) requireSchemaCurrentFix(t *testing.T, fix string, held ...string) {
	t.Helper()
	pass, warn, detail, got, err := schemaCurrentCheck(t, h.syncDSN)
	require.NoError(t, err, "a WARN keeps the doctor's exit code 0")
	require.True(t, pass, detail)
	require.True(t, warn, detail)
	for _, p := range held {
		require.Contains(t, detail, p)
	}
	require.Equal(t, "as the table owner ("+h.owner+"): "+fix, got)
}

// requireSchemaCurrentClean asserts that the schema current check passes
// without a WARN for the syncing role.
func (h *schemaHub) requireSchemaCurrentClean(t *testing.T) {
	t.Helper()
	pass, warn, detail, fix, err := schemaCurrentCheck(t, h.syncDSN)
	require.NoError(t, err)
	require.True(t, pass, detail)
	require.False(t, warn, detail)
	require.Empty(t, fix)
}

// TestDoctorSchemaCurrent_CollisionPrivilegeSources_PrintedRevokeClearsThem:
// for each way a syncing role can hold INSERT on sync_node_collisions or
// USAGE on its sequence through a grant the owner made (to PUBLIC, to a
// role it inherits through two memberships, on one column only, and to a
// role in a schema whose names need quoting), the schema current check
// prints the exact REVOKE statements; run as printed by the table owner,
// they clear it and the check passes (MTIX-95.1.7).
func TestDoctorSchemaCurrent_CollisionPrivilegeSources_PrintedRevokeClearsThem(t *testing.T) {
	const insert, usage = "INSERT on sync_node_collisions", "USAGE on sync_node_collisions_collision_id_seq"
	tests := []struct {
		name   string
		schema string
		setup  func(h *schemaHub) (fix string, held []string)
	}{
		{"a grant to PUBLIC", "hub_data", func(h *schemaHub) (string, []string) {
			h.f.ddl("GRANT INSERT ON TABLE %I.sync_node_collisions TO PUBLIC", h.schema)
			return "REVOKE INSERT ON TABLE hub_data.sync_node_collisions FROM PUBLIC;", []string{insert}
		}},
		{"a grant reached through two memberships", "hub_data", func(h *schemaHub) (string, []string) {
			outer, inner := h.f.role("outer"), h.f.role("inner")
			h.f.ddl("GRANT INSERT ON TABLE %I.sync_node_collisions TO %I", h.schema, outer)
			h.f.ddl("GRANT %I TO %I", outer, inner)
			h.f.ddl("GRANT %I TO %I", inner, h.syncer)
			return "REVOKE INSERT ON TABLE hub_data.sync_node_collisions FROM " + outer + ";", []string{insert}
		}},
		{"a grant on one column", "hub_data", func(h *schemaHub) (string, []string) {
			h.f.ddl("GRANT INSERT (project_prefix) ON TABLE %I.sync_node_collisions TO %I", h.schema, h.syncer)
			return "REVOKE INSERT ON TABLE hub_data.sync_node_collisions FROM " + h.syncer + ";", []string{insert}
		}},
		{"names that need quoting", `Hub "Q" x`, func(h *schemaHub) (string, []string) {
			odd := h.f.role(`x_Odd "Grp" x`)
			h.f.ddl("GRANT INSERT ON TABLE %I.sync_node_collisions TO %I", h.schema, odd)
			h.f.ddl("GRANT USAGE ON SEQUENCE %I.sync_node_collisions_collision_id_seq TO %I", h.schema, odd)
			h.f.ddl("GRANT %I TO %I", odd, h.syncer)
			schema, grantee := h.ident(h.schema), h.ident(odd)
			return "REVOKE INSERT ON TABLE " + schema + ".sync_node_collisions FROM " + grantee + "; " +
					"REVOKE USAGE ON SEQUENCE " + schema + ".sync_node_collisions_collision_id_seq FROM " + grantee + ";",
				[]string{insert, usage}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			h := newSchemaHub(t, tt.schema)
			h.requireSchemaCurrentClean(t)
			fix, held := tt.setup(h)
			h.requireSchemaCurrentFix(t, fix, held...)
			require.NoError(t, h.f.tryAs(h.owner, fix), "the printed fix runs as printed")
			h.requireSchemaCurrentClean(t)
		})
	}
}

// TestDoctorSchemaCurrent_CollisionGrantsHardenClears_PrintsHarden: when
// the syncing role holds INSERT on sync_node_collisions by the grant of a
// role other than the table owner, or holds it with a grant option it has
// passed on, the fix the schema current check prints is mtix sync harden,
// whose --apply revokes with CASCADE (MTIX-95.1.7).
func TestDoctorSchemaCurrent_CollisionGrantsHardenClears_PrintsHarden(t *testing.T) {
	tests := []struct {
		name  string
		setup func(h *schemaHub)
	}{
		{"granted by a role other than the owner", func(h *schemaHub) {
			granter := h.f.role("granter")
			h.f.ddl("GRANT USAGE ON SCHEMA %I TO %I", h.schema, granter)
			h.f.ddl("GRANT INSERT ON TABLE %I.sync_node_collisions TO %I WITH GRANT OPTION", h.schema, granter)
			h.f.ddlAs(granter, "GRANT INSERT ON TABLE %I.sync_node_collisions TO %I", h.schema, h.syncer)
		}},
		{"a grant option passed on", func(h *schemaHub) {
			other := h.f.role("other")
			h.f.ddl("GRANT INSERT ON TABLE %I.sync_node_collisions TO %I WITH GRANT OPTION", h.schema, h.syncer)
			h.f.ddlAs(h.syncer, "GRANT INSERT ON TABLE %I.sync_node_collisions TO %I", h.schema, other)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			h := newSchemaHub(t, "hub_data")
			tt.setup(h)
			h.requireSchemaCurrentFix(t, hubPrivilegesFix, "INSERT on sync_node_collisions")
		})
	}
}

// TestDoctorSchemaCurrent_SetRoleMembership_PrintsHarden: a syncing role
// that does not inherit a role but can SET ROLE to it, where that role
// holds INSERT on sync_node_collisions, is reported, with mtix sync
// harden as the fix (MTIX-95.1.7). PostgreSQL 16 and later.
func TestDoctorSchemaCurrent_SetRoleMembership_PrintsHarden(t *testing.T) {
	initTestApp(t)
	h := newSchemaHub(t, "hub_data")
	if h.f.strings(`SELECT (current_setting('server_version_num')::int >= 160000)::text`)[0] != "true" {
		t.Skip("membership options INHERIT and SET need PostgreSQL 16 or later")
	}
	setOnly := h.f.role("setonly")
	h.f.ddl("GRANT INSERT ON TABLE %I.sync_node_collisions TO %I", h.schema, setOnly)
	h.f.ddl("GRANT %I TO %I WITH INHERIT FALSE, SET TRUE", setOnly, h.syncer)
	require.Equal(t, []string{"false"}, h.f.strings(`SELECT pg_catalog.has_table_privilege($1::text,
		'hub_data.sync_node_collisions', 'INSERT')::text`, h.syncer), "the role does not hold it itself")
	h.requireSchemaCurrentFix(t, hubPrivilegesFix, "INSERT on sync_node_collisions")
}

// TestDoctorSchemaCurrent_HubWithoutRecorder_ReportsOnlyMigration017: on a
// hub whose collision recorder is not there yet, a syncing role holding
// INSERT on sync_node_collisions and USAGE on its sequence, as it needs
// until migration 017, gets only the 017 gap, fixed by mtix sync init, and
// no REVOKE (MTIX-95.1.7).
func TestDoctorSchemaCurrent_HubWithoutRecorder_ReportsOnlyMigration017(t *testing.T) {
	initTestApp(t)
	h := newSchemaHub(t, "hub_data")
	h.f.ddl("GRANT INSERT ON TABLE %I.sync_node_collisions TO %I", h.schema, h.syncer)
	h.f.ddl("GRANT USAGE ON SEQUENCE %I.sync_node_collisions_collision_id_seq TO %I", h.schema, h.syncer)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := h.f.admin.Exec(ctx, `DROP FUNCTION hub_data.record_restore_collision(text, text, text, text, bigint)`)
	require.NoError(t, err)

	pass, warn, detail, fix, err := schemaCurrentCheck(t, h.syncDSN)
	require.NoError(t, err)
	require.True(t, pass, detail)
	require.True(t, warn, detail)
	require.Contains(t, detail, "missing: function record_restore_collision(text, text, text, text, bigint)")
	require.Contains(t, detail, "pushes keep working")
	require.NotContains(t, detail, "INSERT on sync_node_collisions")
	require.NotContains(t, fix, "REVOKE")
	require.Equal(t, "as the table owner ("+h.owner+"): mtix sync init", fix)
}
