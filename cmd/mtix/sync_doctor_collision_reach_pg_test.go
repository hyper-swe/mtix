// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// refusedForSyncer asserts that the statement template builds, with each
// identifier quoted server-side, fails in the syncing role's own login
// session.
func (h *schemaHub) refusedForSyncer(template string, idents ...string) {
	h.f.t.Helper()
	stmt := h.f.strings(`SELECT format($1::text, VARIADIC $2::text[])`, template, idents)[0]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := transport.New(ctx, h.syncDSN, transport.Options{InsecureTLS: true})
	require.NoError(h.f.t, err)
	defer pool.Close()
	_, err = pool.Inner().Exec(ctx, stmt)
	require.Errorf(h.f.t, err, "the syncing role cannot run %s", stmt)
}

// reachPaths are the paths a chain of SET ROLE and ADMIN OPTION opens, each
// named with its chain; ADMIN OPTION on a superuser alone opens none
// (MTIX-95.1.7). SET and ADMIN as grant options exist from PostgreSQL 16.
func reachPaths() []collisionPath {
	return []collisionPath{
		{"ADMIN, then SET to a superuser", "hub_data", pg16, 0, func(h *schemaHub) ([]string, []string, []string) {
			a, su := h.f.role("a"), h.f.role("su")
			h.f.ddl("ALTER ROLE %I SUPERUSER", su)
			h.f.ddl("GRANT %I TO %I", su, a)
			h.f.ddl("GRANT %I TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE", a, h.syncer)
			return nil, []string{"ADMIN OPTION on " + a + ", which can SET ROLE to " + su + ", a superuser"},
				[]string{heldInsert, heldUsage}
		}},
		{"ADMIN, then SET to a holder", "hub_data", pg16, 0, func(h *schemaHub) ([]string, []string, []string) {
			a, holder := h.f.role("a"), h.f.role("holder")
			h.grantTo("INSERT", holder, "")
			h.f.ddl("GRANT %I TO %I WITH INHERIT FALSE, SET TRUE", holder, a)
			h.f.ddl("GRANT %I TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE", a, h.syncer)
			return nil, []string{"ADMIN OPTION on " + a + ", which can SET ROLE to " + holder + ", which holds " +
				heldInsert}, []string{heldInsert}
		}},
		{"ADMIN, then SET to a role inheriting pg_write_all_data", "hub_data", pg16, 0,
			func(h *schemaHub) ([]string, []string, []string) {
				a, w := h.f.role("a"), h.f.role("w")
				h.f.ddl("GRANT pg_write_all_data TO %I", w)
				h.f.ddl("GRANT %I TO %I WITH INHERIT FALSE, SET TRUE", w, a)
				h.f.ddl("GRANT %I TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE", a, h.syncer)
				return nil, []string{
					"ADMIN OPTION on " + a + ", which can SET ROLE to pg_write_all_data, which holds " + heldInsert,
					"ADMIN OPTION on " + a + ", which can SET ROLE to " + w + ", which holds " + heldInsert,
				}, []string{heldInsert}
			}},
		{"SET, then ADMIN, then SET", "hub_data", pg16, 0, func(h *schemaHub) ([]string, []string, []string) {
			a, b, c := h.f.role("a"), h.f.role("b"), h.f.role("c")
			h.grantTo("INSERT", c, "")
			h.f.ddl("GRANT %I TO %I WITH INHERIT FALSE, SET TRUE", c, b)
			h.f.ddl("GRANT %I TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE", b, a)
			h.f.ddl("GRANT %I TO %I WITH INHERIT FALSE, SET TRUE", a, h.syncer)
			return nil, []string{"SET ROLE to " + a + ", which has ADMIN OPTION on " + b + ", which can SET ROLE to " +
				c + ", which holds " + heldInsert}, []string{heldInsert}
		}},
		{"ADMIN held by a role it can neither use nor become", "hub_data", pg16, 0,
			func(h *schemaHub) ([]string, []string, []string) {
				a, b := h.f.role("a"), h.f.role("b")
				h.grantTo("INSERT", b, "")
				h.f.ddl("GRANT %I TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE", b, a)
				h.f.ddl("GRANT %I TO %I WITH ADMIN FALSE, INHERIT FALSE, SET FALSE", a, h.syncer)
				h.refusedForSyncer("SET ROLE %I", a)
				h.refusedForSyncer("GRANT %I TO %I", b, h.syncer)
				return nil, nil, nil // no chain reaches b: the check stays clean
			}},
		{"ADMIN OPTION on a superuser", "hub_data", pg16, 0, func(h *schemaHub) ([]string, []string, []string) {
			su := h.f.role("su")
			h.f.ddl("ALTER ROLE %I SUPERUSER", su)
			h.f.ddl("GRANT %I TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE", su, h.syncer)
			h.refusedForSyncer("GRANT %I TO %I", su, h.syncer)
			return nil, nil, nil // only a superuser grants a superuser role: the check stays clean
		}},
		{"the shorter of two chains", "hub_data", pg16, 0, func(h *schemaHub) ([]string, []string, []string) {
			a, holder := h.f.role("a"), h.f.role("holder")
			h.grantTo("INSERT", holder, "")
			h.f.ddl("GRANT %I TO %I WITH INHERIT FALSE, SET TRUE", holder, a)
			h.f.ddl("GRANT %I TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE", a, h.syncer)
			h.f.ddl("GRANT %I TO %I WITH INHERIT FALSE, SET TRUE", holder, h.syncer)
			return nil, []string{"SET ROLE to " + holder + ", which holds " + heldInsert}, []string{heldInsert}
		}},
		{"no role past a superuser", "hub_data", pg16, 0, func(h *schemaHub) ([]string, []string, []string) {
			su, y, z := h.f.role("su"), h.f.role("y"), h.f.role("z")
			h.f.ddl("ALTER ROLE %I SUPERUSER", su)
			h.f.ddl("GRANT %I TO %I", su, h.syncer)
			h.grantTo("INSERT", y, "")
			h.f.ddl("GRANT %I TO %I WITH ADMIN TRUE", y, z)
			h.f.ddl("GRANT %I TO %I WITH ADMIN FALSE, INHERIT FALSE, SET FALSE", y, h.syncer)
			return nil, []string{"SET ROLE to " + su + ", a superuser"}, []string{heldInsert, heldUsage}
		}},
		{"SET to a role with a column grant", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			member := h.f.role("member")
			h.f.ddl("GRANT INSERT (project_prefix) ON TABLE %I.sync_node_collisions TO %I", h.schema, member)
			h.f.ddl("ALTER ROLE %I NOINHERIT", h.syncer)
			h.f.ddl("GRANT %I TO %I", member, h.syncer)
			return nil, []string{h.reachVerb() + member + ", which holds " + heldInsert}, []string{heldInsert}
		}},
	}
}

// schemaOwnerPaths are the paths ownership of the schema that holds the
// sync tables opens, when another role owns the tables (MTIX-95.1.7).
func schemaOwnerPaths() []collisionPath {
	const where = "ownership of schema "
	return []collisionPath{
		{"the schema, owned", `Hub "Q" x`, 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			h.f.ddl("ALTER SCHEMA %I OWNER TO %I", h.schema, h.syncer)
			return nil, []string{where + h.ident(h.schema) + " that holds the sync tables"}, []string{heldInsert}
		}},
		{"the schema, owned by a role it inherits", "hub_data", 0, 0, func(h *schemaHub) ([]string, []string, []string) {
			team := h.f.role("team")
			h.f.ddl("ALTER SCHEMA %I OWNER TO %I", h.schema, team)
			h.f.ddl("GRANT %I TO %I", team, h.syncer)
			return nil, []string{where + "hub_data that holds the sync tables, inherited from " + team},
				[]string{heldInsert}
		}},
		{"the schema, owned by a role it can SET ROLE to", "hub_data", 0, 0,
			func(h *schemaHub) ([]string, []string, []string) {
				so := h.f.role("so")
				h.f.ddl("ALTER SCHEMA %I OWNER TO %I", h.schema, so)
				h.f.ddl("ALTER ROLE %I NOINHERIT", h.syncer)
				h.f.ddl("GRANT %I TO %I", so, h.syncer)
				return nil, []string{h.reachVerb() + so + ", which owns schema hub_data that holds the sync tables"},
					[]string{heldInsert}
			}},
	}
}

// TestDoctorSchemaCurrent_SuperuserNotTableOwner_Clean: a superuser DSN
// that does not name the table owner leaves the schema current check
// clean: a superuser is outside the collision check (MTIX-95.1.7).
func TestDoctorSchemaCurrent_SuperuserNotTableOwner_Clean(t *testing.T) {
	initTestApp(t)
	h := newSchemaHub(t, "hub_data")
	su := h.f.role("dsnsu")
	h.f.ddl("ALTER ROLE %I SUPERUSER", su)
	require.NotEqual(t, h.owner, su)
	pass, warn, detail, fix, err := schemaCurrentCheck(t, loginRoleDSN(t, h.f, su))
	require.NoError(t, err)
	require.True(t, pass, detail)
	require.False(t, warn, detail)
	require.Empty(t, fix)
}
