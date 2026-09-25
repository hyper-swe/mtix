// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"fmt"
	"sort"
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

// collisionFix is what the schema current check prints: the table owner's
// REVOKE statements, then the named paths, sorted, for a role
// administrator, after collisionPathsIntro (pinned by the unit test).
func (h *schemaHub) collisionFix(owner, paths []string) string {
	var parts []string
	if len(owner) > 0 {
		parts = append(parts, "as the table owner ("+h.owner+"): "+strings.Join(owner, " "))
	}
	if len(paths) > 0 {
		sorted := append([]string(nil), paths...)
		sort.Strings(sorted)
		parts = append(parts, collisionPathsIntro+strings.Join(sorted, "; "))
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

// collisionPath is one way a syncing role can write collision rows, and
// what the schema current check reports for it: the table owner's exact
// REVOKE statements, and the paths it names for a role administrator.
// Only a case with REVOKE statements alone runs its fix.
type collisionPath struct {
	name   string
	schema string
	min    int // the lowest server_version_num the case applies to, 0 for all
	max    int // the highest it applies to, 0 for none
	setup  func(h *schemaHub) (owner, paths, held []string)
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

// grantBy makes grantor, with USAGE on the schema and INSERT on the
// collision table WITH GRANT OPTION from from (the table owner when
// from is ""), grant INSERT on it to grantee, with extra appended.
func (h *schemaHub) grantBy(from, grantor, grantee, extra string) {
	h.f.t.Helper()
	h.f.ddl("GRANT USAGE ON SCHEMA %I TO %I", h.schema, grantor)
	if from == "" {
		h.grantTo("INSERT", grantor, "WITH GRANT OPTION")
	}
	h.f.ddlAs(grantor, "GRANT INSERT ON TABLE %I.sync_node_collisions TO %I "+extra, h.schema, grantee)
}

// versionNum is the fixture server's server_version_num.
func (h *schemaHub) versionNum() int {
	h.f.t.Helper()
	n := 0
	_, err := fmt.Sscan(h.f.strings(`SELECT current_setting('server_version_num')`)[0], &n)
	require.NoError(h.f.t, err)
	return n
}

// reachVerb is how the check names a role the syncing role can SET ROLE
// to: SET ROLE from PostgreSQL 16, membership before.
func (h *schemaHub) reachVerb() string {
	if h.versionNum() >= 160000 {
		return "SET ROLE to "
	}
	return "membership in "
}

const (
	heldInsert = "INSERT on sync_node_collisions"
	heldUsage  = "USAGE on sync_node_collisions_collision_id_seq"
	pg16       = 160000
)

// grantPaths are the paths a grant opens (MTIX-95.1.7).
func grantPaths() []collisionPath {
	const revoke = "REVOKE INSERT ON TABLE hub_data.sync_node_collisions FROM "
	return []collisionPath{
		{"a grant from the table owner", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			h.grantTo("INSERT", h.syncer, "")
			return []string{revoke + h.syncer + ";"}, nil, []string{heldInsert}
		}},
		{"a grant to PUBLIC", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			h.grantTo("INSERT", "PUBLIC", "")
			return []string{revoke + "PUBLIC;"}, nil, []string{heldInsert}
		}},
		{"a grant to a role it inherits", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			team := h.f.role("team")
			h.grantTo("INSERT", team, "")
			h.f.ddl("GRANT %I TO %I", team, h.syncer)
			return []string{revoke + team + ";"}, nil, []string{heldInsert}
		}},
		{"a grant on one column", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("GRANT INSERT (project_prefix) ON TABLE %I.sync_node_collisions TO %I", h.schema, h.syncer)
			return []string{revoke + h.syncer + ";"}, nil, []string{heldInsert}
		}},
		{"names that need quoting", `Hub "Q" x`, 0, 0, func(h *schemaHub) ([]string, []string, []string) {
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
		{"a grant option from the table owner", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			h.grantTo("INSERT", h.syncer, "WITH GRANT OPTION")
			return nil, []string{heldInsert + " granted to " + h.syncer + " by " + h.owner + " with grant option"},
				[]string{heldInsert}
		}},
		{"a non-superuser grantor", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			granter := h.f.role("granter")
			h.grantBy("", granter, h.syncer, "")
			return nil, []string{heldInsert + " granted to " + h.syncer + " by " + granter}, []string{heldInsert}
		}},
		{"a two-hop grant chain", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			first, second := h.f.role("first"), h.f.role("second")
			h.grantBy("", first, second, "WITH GRANT OPTION")
			h.grantBy(first, second, h.syncer, "")
			return nil, []string{heldInsert + " granted to " + h.syncer + " by " + second}, []string{heldInsert}
		}},
		{"a grant with a grant made from it", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			granter := h.f.role("granter")
			h.grantTo("INSERT", h.syncer, "")
			h.grantBy("", granter, h.syncer, "WITH GRANT OPTION")
			h.f.ddlAs(h.syncer, "GRANT INSERT ON TABLE %I.sync_node_collisions TO %I", h.schema, h.f.role("other"))
			return nil, []string{
				heldInsert + " granted to " + h.syncer + " by " + h.owner,
				heldInsert + " granted to " + h.syncer + " by " + granter + " with grant option",
			}, []string{heldInsert}
		}},
		{"two grantors", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			granter := h.f.role("granter")
			h.grantTo("INSERT", h.syncer, "")
			h.grantBy("", granter, h.syncer, "")
			return []string{revoke + h.syncer + ";"},
				[]string{heldInsert + " granted to " + h.syncer + " by " + granter}, []string{heldInsert}
		}},
	}
}

// serverRolePath is how the check names membership in role, a role that
// writes server files or runs server programs (MTIX-95.1.7).
func serverRolePath(role string) string {
	return "membership in " + role + ", which writes server files or runs server programs"
}

// memberPaths are the paths a role membership or a role attribute opens
// (MTIX-95.1.7). Membership in a server file or program role is named
// whether or not the role inherits it.
func memberPaths() []collisionPath {
	return []collisionPath{
		{"pg_write_all_data", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("GRANT pg_write_all_data TO %I", h.syncer)
			return nil, []string{heldInsert + " inherited from pg_write_all_data"}, []string{heldInsert}
		}},
		{"a default membership in a superuser", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			su := h.f.role("su")
			h.f.ddl("ALTER ROLE %I SUPERUSER", su)
			h.f.ddl("GRANT %I TO %I", su, h.syncer)
			return nil, []string{h.reachVerb() + su + ", a superuser"}, []string{heldInsert, heldUsage}
		}},
		{"a server-program role", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("GRANT pg_execute_server_program TO %I", h.syncer)
			return nil, []string{serverRolePath("pg_execute_server_program")}, []string{heldInsert}
		}},
		{"a server-program role it does not inherit", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("ALTER ROLE %I NOINHERIT", h.syncer)
			h.f.ddl("GRANT pg_execute_server_program TO %I", h.syncer)
			return nil, []string{serverRolePath("pg_execute_server_program")}, []string{heldInsert}
		}},
		{"a server-program membership WITH INHERIT FALSE", "hub_data", pg16, 0,
			func(h *schemaHub) ([]string, []string, []string) {
				h.f.ddl("GRANT pg_execute_server_program TO %I WITH INHERIT FALSE", h.syncer)
				return nil, []string{serverRolePath("pg_execute_server_program")}, []string{heldInsert}
			}},
		{"a server-file role", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("GRANT pg_write_server_files TO %I", h.syncer)
			return nil, []string{serverRolePath("pg_write_server_files")}, []string{heldInsert}
		}},
		{"CREATEROLE before PostgreSQL 16", "hub_data", 0, pg16 - 1, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("ALTER ROLE %I CREATEROLE", h.syncer)
			return nil, []string{"CREATEROLE, which before PostgreSQL 16 lets the role grant itself any role " +
				"but a superuser"}, []string{heldInsert}
		}},
		{"CREATEROLE from PostgreSQL 16", "hub_data", pg16, 0, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("ALTER ROLE %I CREATEROLE", h.syncer)
			return nil, nil, nil // it reaches no role it was not given: the check stays clean
		}},
		{"a membership of a role that does not inherit", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			member := h.f.role("member")
			h.grantTo("INSERT", member, "")
			h.f.ddl("ALTER ROLE %I NOINHERIT", h.syncer)
			h.f.ddl("GRANT %I TO %I", member, h.syncer)
			return nil, []string{h.reachVerb() + member + ", which holds " + heldInsert}, []string{heldInsert}
		}},
		{"SET through an intermediate role", "hub_data", pg16, 0, func(h *schemaHub) ([]string, []string, []string) {
			target, mid := h.f.role("target"), h.f.role("mid")
			h.grantTo("INSERT", target, "")
			h.f.ddl("GRANT %I TO %I WITH INHERIT FALSE, SET TRUE", target, mid)
			h.f.ddl("GRANT %I TO %I", mid, h.syncer)
			return nil, []string{"SET ROLE to " + target + ", which holds " + heldInsert}, []string{heldInsert}
		}},
		{"ADMIN through an intermediate role", "hub_data", pg16, 0, func(h *schemaHub) ([]string, []string, []string) {
			target, mid := h.f.role("target"), h.f.role("mid")
			h.grantTo("INSERT", target, "")
			h.f.ddl("GRANT %I TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE", target, mid)
			h.f.ddl("GRANT %I TO %I", mid, h.syncer)
			return nil, []string{"ADMIN OPTION on " + target + ", which holds " + heldInsert}, []string{heldInsert}
		}},
		{"a SET-only membership in the table owner", "hub_data", pg16, 0, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("GRANT %I TO %I WITH INHERIT FALSE, SET TRUE", h.owner, h.syncer)
			return nil, []string{"SET ROLE to " + h.owner + ", which holds " + heldInsert,
				"SET ROLE to " + h.owner + ", which holds " + heldUsage}, []string{heldInsert, heldUsage}
		}},
		{"a membership without INHERIT, SET or ADMIN", "hub_data", pg16, 0, func(h *schemaHub) ([]string, []string, []string) {
			bare := h.f.role("bare")
			h.grantTo("INSERT", bare, "")
			h.f.ddl("GRANT %I TO %I WITH ADMIN FALSE, INHERIT FALSE, SET FALSE", bare, h.syncer)
			return nil, nil, nil // nothing reaches the privilege: the check stays clean
		}},
	}
}

// TestDoctorSchemaCurrent_CollisionPaths_ReportedWithTheirFix: for each way
// a syncing role can write collision rows (a grant, inherited privilege, a
// role it reaches through a chain of SET ROLE and ADMIN OPTION, a
// superuser it can SET ROLE to, ownership of the tables' schema,
// CREATEROLE before PostgreSQL 16, a server file or program role), the
// schema current check warns and reports it: the exact REVOKE for the
// table owner's own plain grant, which run as printed clears the check,
// and the path by name, with its chain, for a role administrator for
// everything else. A case that reaches nothing stays clean (MTIX-95.1.7).
func TestDoctorSchemaCurrent_CollisionPaths_ReportedWithTheirFix(t *testing.T) {
	cases := append(grantPaths(), memberPaths()...)
	cases = append(cases, reachPaths()...)
	for _, tt := range append(cases, schemaOwnerPaths()...) {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			h := newSchemaHub(t, tt.schema)
			if v := h.versionNum(); v < tt.min || (tt.max > 0 && v > tt.max) {
				t.Skipf("the case applies to server_version_num %d to %d", tt.min, tt.max)
			}
			h.requireSchemaCurrentClean(t)
			owner, paths, held := tt.setup(h)
			if len(owner)+len(paths) == 0 {
				h.requireSchemaCurrentClean(t)
				return
			}
			h.requireSchemaCurrentFix(t, h.collisionFix(owner, paths), held...)
			if len(paths) > 0 {
				return // a role administrator removes a named path
			}
			require.NoError(t, h.f.tryAs(h.owner, strings.Join(owner, " ")), "the printed REVOKE runs as printed")
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
