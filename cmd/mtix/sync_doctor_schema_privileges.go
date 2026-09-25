// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// collisionPrivilegesSQL reads every path by which the connecting role can
// write collision rows, through INSERT on sync_node_collisions (on any of
// its columns included) or USAGE on its sequence, using PostgreSQL's own
// privilege functions (MTIX-95.1.7). The table owner, a role that inherits
// it and a superuser are left out: ownership brings those privileges
// (pg_has_role also gives a superuser the privileges of every role, so
// the owner test leaves a superuser out too).
// Kind 1 is the exact REVOKE of a plain grant the table owner made (no
// grant option, no grant made from it), built server-side by format()
// (SQL Rule 1a); kind 2 names any other path, for a role administrator.
//
//   - held: each object, its schema and the schema's owner, and whether
//     the role holds the privilege (has_any_column_privilege,
//     has_sequence_privilege), predefined roles such as pg_write_all_data
//     included; holds: every role that holds it.
//   - acl, mine: every grant of the privilege on the object, table or
//     column; mine are those to PUBLIC or to a role the connecting role
//     inherits, which the held privilege comes from.
//   - reach: the transitive closure of the roles the connecting role can
//     become, each with the chain that reaches it. A SET edge from a
//     reached role r adds a role r can SET ROLE to (a member of, before
//     PostgreSQL 16); an ADMIN edge adds a role that is not a superuser
//     and on which r holds ADMIN OPTION it can use, the admin option of a
//     role whose privileges r has: r grants that role to itself. No role
//     is reached past a superuser. Every role reached is one the
//     connecting role is a member of, which bounds the search; two SET
//     edges in a row are one, and each chain visits a role once.
//   - chains: the shortest chain to each role reached.
//   - paths: the owner's plain grants (kind 1); every other grant, named
//     with its grantee and grantor; a predefined role inherited without a
//     grant; ownership of the tables' schema, directly or inherited; each
//     role reached that is a superuser, or that it does not inherit and
//     that holds the privilege or owns the tables' schema (a role it
//     inherits is covered by held); CREATEROLE before PostgreSQL 16; and
//     membership in pg_execute_server_program or pg_write_server_files.
const collisionPrivilegesSQL = `
	WITH RECURSIVE me AS (
	    SELECT r.oid, r.rolsuper, r.rolcreaterole,
	           pg_catalog.current_setting('server_version_num')::int >= 160000 AS pg16
	    FROM pg_catalog.pg_roles r WHERE r.rolname = current_user),
	held AS (
	    SELECT o.label, o.priv, o.kind, c.oid AS rel, c.relowner AS owner, c.relacl AS acl,
	           n.nspname AS schema_name, n.nspowner AS schema_owner, c.relname AS rel_name,
	           CASE WHEN o.kind = 'TABLE' THEN pg_catalog.has_any_column_privilege(me.oid, c.oid, 'INSERT')
	                ELSE pg_catalog.has_sequence_privilege(me.oid, c.oid, 'USAGE') END AS direct
	    FROM (VALUES
	          ('INSERT on sync_node_collisions', pg_catalog.to_regclass('sync_node_collisions'), 'INSERT', 'TABLE'),
	          ('USAGE on sync_node_collisions_collision_id_seq',
	           pg_catalog.to_regclass('sync_node_collisions_collision_id_seq'), 'USAGE', 'SEQUENCE'))
	         AS o(label, rel, priv, kind)
	    CROSS JOIN me
	    JOIN pg_catalog.pg_class c ON c.oid = o.rel
	    JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	    WHERE NOT me.rolsuper AND NOT pg_catalog.pg_has_role(me.oid, c.relowner, 'USAGE')),
	holds AS (
	    SELECT h.label, x.oid
	    FROM held h CROSS JOIN pg_catalog.pg_roles x
	    WHERE CASE WHEN h.kind = 'TABLE' THEN pg_catalog.has_any_column_privilege(x.oid, h.rel, 'INSERT')
	               ELSE pg_catalog.has_sequence_privilege(x.oid, h.rel, 'USAGE') END),
	acl AS (
	    SELECT h.label, x.grantee, x.grantor, x.is_grantable
	    FROM held h CROSS JOIN LATERAL pg_catalog.aclexplode(h.acl) x WHERE x.privilege_type = h.priv
	    UNION ALL
	    SELECT h.label, x.grantee, x.grantor, x.is_grantable
	    FROM held h JOIN pg_catalog.pg_attribute att
	         ON att.attrelid = h.rel AND att.attacl IS NOT NULL AND NOT att.attisdropped
	    CROSS JOIN LATERAL pg_catalog.aclexplode(att.attacl) x WHERE x.privilege_type = h.priv),
	mine AS (
	    SELECT a.label, a.grantee, a.grantor, a.is_grantable,
	           CASE WHEN a.grantee = 0 THEN 'PUBLIC'
	                ELSE pg_catalog.quote_ident(pg_catalog.pg_get_userbyid(a.grantee)::text) END AS grantee_name,
	           a.grantor = h.owner AND NOT a.is_grantable
	               AND NOT EXISTS (SELECT 1 FROM acl d WHERE d.label = a.label AND d.grantor = a.grantee) AS plain
	    FROM acl a JOIN held h ON h.label = a.label CROSS JOIN me
	    WHERE CASE WHEN a.grantee = 0 THEN true ELSE pg_catalog.pg_has_role(me.oid, a.grantee, 'USAGE') END),
	reach AS (
	    SELECT me.oid, me.rolsuper, 'start'::text AS edge, ARRAY[me.oid] AS seen, ''::text AS chain
	    FROM me
	    UNION ALL
	    SELECT x.oid, x.rolsuper, s.edge, r.seen || x.oid,
	           r.chain || pg_catalog.format(CASE
	               WHEN s.edge = 'admin' AND r.chain = '' THEN 'ADMIN OPTION on %I'
	               WHEN s.edge = 'admin' THEN ', which has ADMIN OPTION on %I'
	               WHEN me.pg16 AND r.chain = '' THEN 'SET ROLE to %I'
	               WHEN me.pg16 THEN ', which can SET ROLE to %I'
	               WHEN r.chain = '' THEN 'membership in %I'
	               ELSE ', which is a member of %I' END, x.rolname::text COLLATE "default")
	    FROM reach r CROSS JOIN me
	    JOIN pg_catalog.pg_roles x
	         ON x.oid <> ALL (r.seen) AND pg_catalog.pg_has_role(me.oid, x.oid, 'MEMBER')
	    CROSS JOIN LATERAL (SELECT CASE
	        WHEN r.edge <> 'set' AND CASE WHEN me.pg16 THEN pg_catalog.pg_has_role(r.oid, x.oid, 'SET')
	                                      ELSE pg_catalog.pg_has_role(r.oid, x.oid, 'MEMBER') END THEN 'set'
	        WHEN NOT x.rolsuper AND EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members m
	                                        WHERE m.roleid = x.oid AND m.admin_option
	                                          AND pg_catalog.pg_has_role(r.oid, m.member, 'USAGE')) THEN 'admin'
	        END AS edge) s
	    WHERE NOT r.rolsuper AND s.edge IS NOT NULL),
	chains AS (
	    SELECT DISTINCT ON (r.oid) r.oid, r.rolsuper, r.chain
	    FROM reach r WHERE r.chain <> ''
	    ORDER BY r.oid, pg_catalog.cardinality(r.seen), r.chain),
	paths AS (
	    SELECT m.label, 1 AS kind, pg_catalog.format('REVOKE %s ON %s %I.%I FROM %s;',
	               h.priv, h.kind, h.schema_name, h.rel_name, m.grantee_name) AS text
	    FROM mine m JOIN held h ON h.label = m.label WHERE m.plain
	    UNION
	    SELECT m.label, 2, pg_catalog.format('%s granted to %s by %I%s', m.label, m.grantee_name,
	               pg_catalog.pg_get_userbyid(m.grantor)::text,
	               CASE WHEN m.is_grantable THEN ' with grant option' ELSE '' END)
	    FROM mine m WHERE NOT m.plain
	    UNION
	    SELECT h.label, 2, pg_catalog.format('%s inherited from %I', h.label, x.rolname::text)
	    FROM held h CROSS JOIN me JOIN holds ho ON ho.label = h.label
	    JOIN pg_catalog.pg_roles x ON x.oid = ho.oid AND x.oid <> me.oid
	    WHERE x.oid < 16384 AND NOT x.rolsuper AND pg_catalog.pg_has_role(me.oid, x.oid, 'USAGE')
	      AND NOT EXISTS (SELECT 1 FROM acl a WHERE a.label = h.label AND a.grantee = x.oid)
	    UNION
	    SELECT h.label, 2, pg_catalog.format('ownership of schema %I that holds the sync tables%s', h.schema_name,
	               CASE WHEN h.schema_owner = me.oid THEN ''
	                    ELSE pg_catalog.format(', inherited from %I',
	                                           pg_catalog.pg_get_userbyid(h.schema_owner)::text) END)
	    FROM held h CROSS JOIN me
	    WHERE h.kind = 'TABLE' AND pg_catalog.pg_has_role(me.oid, h.schema_owner, 'USAGE')
	    UNION
	    SELECT h.label, 2, pg_catalog.format('%s, %s', c.chain, CASE
	               WHEN c.rolsuper THEN 'a superuser'
	               WHEN ho.oid IS NOT NULL THEN 'which holds ' || h.label
	               ELSE pg_catalog.format('which owns schema %I that holds the sync tables', h.schema_name) END)
	    FROM held h CROSS JOIN me CROSS JOIN chains c
	    LEFT JOIN holds ho ON ho.label = h.label AND ho.oid = c.oid
	    WHERE c.rolsuper OR (NOT pg_catalog.pg_has_role(me.oid, c.oid, 'USAGE')
	          AND (ho.oid IS NOT NULL OR (h.kind = 'TABLE' AND c.oid = h.schema_owner)))
	    UNION
	    SELECT h.label, 2, 'CREATEROLE, which before PostgreSQL 16 lets the role grant itself any role but a superuser'
	    FROM held h CROSS JOIN me WHERE h.kind = 'TABLE' AND me.rolcreaterole AND NOT me.pg16
	    UNION
	    SELECT h.label, 2, pg_catalog.format('membership in %I, which writes server files or runs server programs',
	               x.rolname::text)
	    FROM held h CROSS JOIN me JOIN pg_catalog.pg_roles x
	         ON x.rolname IN ('pg_execute_server_program', 'pg_write_server_files')
	    WHERE h.kind = 'TABLE' AND pg_catalog.pg_has_role(me.oid, x.oid, 'MEMBER'))
	SELECT h.label, COALESCE(p.kind, 0), COALESCE(p.text, '')
	FROM held h LEFT JOIN paths p ON p.label = h.label
	WHERE h.direct OR p.label IS NOT NULL
	ORDER BY 1, 2, 3`

// collisionPrivilegeRow is one row of collisionPrivilegesSQL: a privilege
// the connecting role holds or can reach, and a REVOKE (kind 1), a named
// path (kind 2) or neither (kind 0, empty text) (MTIX-95.1.7).
type collisionPrivilegeRow struct {
	label string
	kind  int
	text  string
}

// readCollisionPrivileges returns the privileges on sync_node_collisions
// and its sequence that the connecting role holds or can reach beyond the
// least-privilege list, the exact REVOKE statements the table owner runs
// for its plain grants, and every other path by name, sorted, for a role
// administrator (MTIX-95.1.7), as collectCollisionPrivileges lists them.
func readCollisionPrivileges(ctx context.Context, pool *transport.Pool) (held, revokes, paths []string, err error) {
	// Each privilege the role holds or reaches, with each REVOKE and each
	// named path (collisionPrivilegesSQL).
	rows, err := pool.Inner().Query(ctx, collisionPrivilegesSQL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read sync_node_collisions privileges: %w", err)
	}
	defer rows.Close()
	var read []collisionPrivilegeRow
	for rows.Next() {
		var r collisionPrivilegeRow
		if err := rows.Scan(&r.label, &r.kind, &r.text); err != nil {
			return nil, nil, nil, fmt.Errorf("read sync_node_collisions privileges: %w", err)
		}
		read = append(read, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("read sync_node_collisions privileges: %w", err)
	}
	held, revokes, paths = collectCollisionPrivileges(read)
	return held, revokes, paths, nil
}

// collectCollisionPrivileges lists the privileges of rows in order, the
// REVOKE statements and the named paths, sorted, each once (MTIX-95.1.7).
// collisionPrivilegesSQL names a path for every source of a privilege it
// knows; a privilege whose rows name none is named itself, so every
// privilege held appears in the fix, whatever grants it.
func collectCollisionPrivileges(rows []collisionPrivilegeRow) (held, revokes, paths []string) {
	labels, texts, explained := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		if !labels[r.label] {
			labels[r.label] = true
			held = append(held, r.label)
		}
		if r.text == "" {
			continue
		}
		explained[r.label] = true
		if texts[r.text] {
			continue
		}
		texts[r.text] = true
		if r.kind == 1 {
			revokes = append(revokes, r.text)
		} else {
			paths = append(paths, r.text)
		}
	}
	for _, label := range held {
		if !explained[label] {
			paths = append(paths, unnamedPath(label))
		}
	}
	sort.Strings(paths)
	return held, revokes, paths
}

// unnamedPath names a privilege the role holds by a path the check does
// not name (MTIX-95.1.7).
func unnamedPath(label string) string {
	return label + " held through a grant or a role membership"
}
