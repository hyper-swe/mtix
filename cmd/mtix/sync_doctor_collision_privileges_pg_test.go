// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"strings"
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

// pg16 reports whether the fixture server is PostgreSQL 16 or later, where
// a membership carries its own INHERIT, SET and ADMIN options.
func (h *schemaHub) pg16() bool {
	h.f.t.Helper()
	return h.f.strings(`SELECT (current_setting('server_version_num')::int >= 160000)::text`)[0] == "true"
}

// membershipRevoke is the statement a role administrator runs to remove
// the syncing role's membership in role, granted by the test's superuser:
// with GRANTED BY from PostgreSQL 16, where one membership may have been
// granted by several roles.
func (h *schemaHub) membershipRevoke(role string) string {
	h.f.t.Helper()
	stmt := "REVOKE " + h.ident(role) + " FROM " + h.ident(h.syncer)
	if h.pg16() {
		stmt += " GRANTED BY " + h.ident(h.f.superuser)
	}
	return stmt + ";"
}

// collisionFix is what the schema current check prints for the owner's
// statements and the role administrator's statements.
func (h *schemaHub) collisionFix(owner, admin []string) string {
	var parts []string
	if len(owner) > 0 {
		parts = append(parts, "as the table owner ("+h.owner+"): "+strings.Join(owner, " "))
	}
	if len(admin) > 0 {
		parts = append(parts, "as a role administrator: "+strings.Join(admin, " "))
	}
	return strings.Join(parts, ", then ")
}

// requireSchemaCurrentFix asserts that the schema current check of a
// doctor run with the syncing role's DSN warns, names each of held, and
// prints fix.
func (h *schemaHub) requireSchemaCurrentFix(t *testing.T, fix string, held ...string) {
	t.Helper()
	pass, warn, detail, got, err := schemaCurrentCheck(t, h.syncDSN)
	require.NoError(t, err, "a WARN keeps the doctor's exit code 0")
	require.True(t, pass, detail)
	require.True(t, warn, detail)
	for _, p := range held {
		require.Contains(t, detail, p)
	}
	require.Equal(t, fix, got)
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

// collisionPath is one way a syncing role can hold or reach INSERT on
// sync_node_collisions or USAGE on its sequence, and the statements the
// schema current check prints for it: run by the table owner, and run by
// a role administrator.
type collisionPath struct {
	name   string
	schema string
	pg16   bool // needs the membership options of PostgreSQL 16
	setup  func(h *schemaHub) (owner, admin, held []string)
}

// grantTo gives role (or PUBLIC) privilege on the collision table or its
// sequence in the hub's schema, as the table owner would, with extra
// appended.
func (h *schemaHub) grantTo(privilege, role, extra string) {
	h.f.t.Helper()
	object := "TABLE %I.sync_node_collisions"
	if privilege == "USAGE" {
		object = "SEQUENCE %I.sync_node_collisions_collision_id_seq"
	}
	if role == "PUBLIC" {
		h.f.ddl("GRANT "+privilege+" ON "+object+" TO PUBLIC "+extra, h.schema)
		return
	}
	h.f.ddl("GRANT "+privilege+" ON "+object+" TO %I "+extra, h.schema, role)
}

const (
	heldInsert = "INSERT on sync_node_collisions"
	heldUsage  = "USAGE on sync_node_collisions_collision_id_seq"
)

// grantCollisionPaths are the paths a grant on the objects opens; the
// table owner's REVOKE statements clear each (MTIX-95.1.7).
func grantCollisionPaths() []collisionPath {
	const revoke = "REVOKE INSERT ON TABLE hub_data.sync_node_collisions FROM "
	return []collisionPath{
		{"a grant to PUBLIC", "hub_data", false, func(h *schemaHub) ([]string, []string, []string) {
			h.grantTo("INSERT", "PUBLIC", "")
			return []string{revoke + "PUBLIC;"}, nil, []string{heldInsert}
		}},
		{"a grant reached through two memberships", "hub_data", false, func(h *schemaHub) ([]string, []string, []string) {
			outer, inner := h.f.role("outer"), h.f.role("inner")
			h.grantTo("INSERT", outer, "")
			h.f.ddl("GRANT %I TO %I", outer, inner)
			h.f.ddl("GRANT %I TO %I", inner, h.syncer)
			return []string{revoke + outer + ";"}, nil, []string{heldInsert}
		}},
		{"a grant on one column", "hub_data", false, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("GRANT INSERT (project_prefix) ON TABLE %I.sync_node_collisions TO %I", h.schema, h.syncer)
			return []string{revoke + h.syncer + ";"}, nil, []string{heldInsert}
		}},
		{"a table grant and a column grant to the same role", "hub_data", false, func(h *schemaHub) ([]string, []string, []string) {
			h.grantTo("INSERT", h.syncer, "")
			h.f.ddl("GRANT INSERT (display_path) ON TABLE %I.sync_node_collisions TO %I", h.schema, h.syncer)
			return []string{revoke + h.syncer + ";"}, nil, []string{heldInsert}
		}},
		{"names that need quoting", `Hub "Q" x`, false, func(h *schemaHub) ([]string, []string, []string) {
			odd := h.f.role(`x_Odd "Grp" x`)
			h.grantTo("INSERT", odd, "")
			h.grantTo("USAGE", odd, "")
			h.f.ddl("GRANT %I TO %I", odd, h.syncer)
			schema, grantee := h.ident(h.schema), h.ident(odd)
			return []string{
				"REVOKE INSERT ON TABLE " + schema + ".sync_node_collisions FROM " + grantee + ";",
				"REVOKE USAGE ON SEQUENCE " + schema + ".sync_node_collisions_collision_id_seq FROM " + grantee + ";",
			}, nil, []string{heldInsert, heldUsage}
		}},
		{"a grant made by a role other than the owner", "hub_data", false, func(h *schemaHub) ([]string, []string, []string) {
			granter := h.f.role("granter")
			h.f.ddl("GRANT USAGE ON SCHEMA %I TO %I", h.schema, granter)
			h.grantTo("INSERT", granter, "WITH GRANT OPTION")
			h.f.ddlAs(granter, "GRANT INSERT ON TABLE %I.sync_node_collisions TO %I", h.schema, h.syncer)
			return []string{"REVOKE GRANT OPTION FOR INSERT ON TABLE hub_data.sync_node_collisions FROM " +
				granter + " CASCADE;"}, nil, []string{heldInsert}
		}},
		{"a grant option passed on", "hub_data", false, func(h *schemaHub) ([]string, []string, []string) {
			h.grantTo("INSERT", h.syncer, "WITH GRANT OPTION")
			h.f.ddlAs(h.syncer, "GRANT INSERT ON TABLE %I.sync_node_collisions TO %I", h.schema, h.f.role("other"))
			return []string{revoke + h.syncer + " CASCADE;"}, nil, []string{heldInsert}
		}},
		{"a column grant option passed on", "hub_data", false, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("GRANT INSERT (project_prefix) ON TABLE %I.sync_node_collisions TO %I WITH GRANT OPTION",
				h.schema, h.syncer)
			h.f.ddlAs(h.syncer, "GRANT INSERT (project_prefix) ON TABLE %I.sync_node_collisions TO %I",
				h.schema, h.f.role("other"))
			h.f.ddl("GRANT INSERT (actor) ON TABLE %I.audit_log TO %I", h.schema, h.syncer)
			return []string{revoke + h.syncer + " CASCADE;"}, nil, []string{heldInsert}
		}},
	}
}

// membershipCollisionPaths are the paths a role membership opens; a role
// administrator's membership REVOKE clears each (MTIX-95.1.7).
func membershipCollisionPaths() []collisionPath {
	return []collisionPath{
		{"membership in pg_write_all_data", "hub_data", false, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("GRANT pg_write_all_data TO %I", h.syncer)
			return nil, []string{h.membershipRevoke("pg_write_all_data")}, []string{heldInsert}
		}},
		{"pg_write_all_data through another role", "hub_data", false, func(h *schemaHub) ([]string, []string, []string) {
			writers := h.f.role("writers")
			h.f.ddl("GRANT pg_write_all_data TO %I", writers)
			h.f.ddl("GRANT %I TO %I", writers, h.syncer)
			return nil, []string{h.membershipRevoke(writers)}, []string{heldInsert}
		}},
		{"a membership of a role that does not inherit", "hub_data", false, func(h *schemaHub) ([]string, []string, []string) {
			member := h.f.role("member")
			h.grantTo("INSERT", member, "")
			h.f.ddl("ALTER ROLE %I NOINHERIT", h.syncer)
			h.f.ddl("GRANT %I TO %I", member, h.syncer)
			return nil, []string{h.membershipRevoke(member)}, []string{heldInsert}
		}},
		{"a SET-only membership in the table owner", "hub_data", true, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("GRANT %I TO %I WITH INHERIT FALSE, SET TRUE", h.owner, h.syncer)
			return nil, []string{h.membershipRevoke(h.owner)}, []string{heldInsert, heldUsage}
		}},
		{"a direct grant and a SET path", "hub_data", true, func(h *schemaHub) ([]string, []string, []string) {
			setOnly := h.f.role("setonly")
			h.grantTo("INSERT", h.syncer, "")
			h.grantTo("INSERT", setOnly, "")
			h.f.ddl("GRANT %I TO %I WITH INHERIT FALSE, SET TRUE", setOnly, h.syncer)
			return []string{"REVOKE INSERT ON TABLE hub_data.sync_node_collisions FROM " + h.syncer + ";"},
				[]string{h.membershipRevoke(setOnly)}, []string{heldInsert}
		}},
		{"a SET-only membership reaching the sequence", "hub_data", true, func(h *schemaHub) ([]string, []string, []string) {
			setOnly := h.f.role("setonly")
			h.grantTo("USAGE", setOnly, "")
			h.f.ddl("GRANT %I TO %I WITH INHERIT FALSE, SET TRUE", setOnly, h.syncer)
			return nil, []string{h.membershipRevoke(setOnly)}, []string{heldUsage}
		}},
		{"an ADMIN-only membership", "hub_data", true, func(h *schemaHub) ([]string, []string, []string) {
			adminOnly := h.f.role("adminonly")
			h.grantTo("INSERT", adminOnly, "")
			h.f.ddl("GRANT %I TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE", adminOnly, h.syncer)
			return nil, []string{h.membershipRevoke(adminOnly)}, []string{heldInsert}
		}},
		{"a membership without INHERIT, SET or ADMIN", "hub_data", true, func(h *schemaHub) ([]string, []string, []string) {
			bare := h.f.role("bare")
			h.grantTo("INSERT", bare, "")
			h.f.ddl("GRANT %I TO %I WITH ADMIN FALSE, INHERIT FALSE, SET FALSE", bare, h.syncer)
			return nil, nil, nil // nothing reaches the privilege: the check stays clean
		}},
	}
}

// TestDoctorSchemaCurrent_CollisionPrivilegePaths_PrintedFixClearsThem: for
// each way a syncing role can hold or reach INSERT on sync_node_collisions
// or USAGE on its sequence, the schema current check warns, names the
// privilege and prints the exact fix: the table owner's REVOKE statements
// (CASCADE for a grant option, REVOKE GRANT OPTION ... CASCADE from the
// role the owner granted it to for a grant another role made), each once,
// and a role administrator's membership REVOKE for a path through a role
// membership. Run as printed, by those roles, the fix clears the check. A
// membership with neither INHERIT, SET nor ADMIN reaches nothing and stays
// clean (MTIX-95.1.7).
func TestDoctorSchemaCurrent_CollisionPrivilegePaths_PrintedFixClearsThem(t *testing.T) {
	for _, tt := range append(grantCollisionPaths(), membershipCollisionPaths()...) {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			h := newSchemaHub(t, tt.schema)
			if tt.pg16 && !h.pg16() {
				t.Skip("membership options INHERIT, SET and ADMIN need PostgreSQL 16 or later")
			}
			h.requireSchemaCurrentClean(t)
			owner, admin, held := tt.setup(h)
			if len(owner)+len(admin) == 0 {
				h.requireSchemaCurrentClean(t)
				return
			}
			h.requireSchemaCurrentFix(t, h.collisionFix(owner, admin), held...)
			if len(owner) > 0 {
				require.NoError(t, h.f.tryAs(h.owner, strings.Join(owner, " ")), "the owner's statements run as printed")
			}
			if len(admin) > 0 {
				h.f.exec(strings.Join(admin, " "))
			}
			h.requireSchemaCurrentClean(t)
		})
	}
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
