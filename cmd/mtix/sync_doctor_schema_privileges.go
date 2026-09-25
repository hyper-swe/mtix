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
// it and a superuser are left out: ownership brings those privileges.
// Kind 1 is the exact REVOKE of a plain grant the table owner made (no
// grant option, no grant made from it), built server-side by format()
// (SQL Rule 1a); kind 2 names any other path, for a role administrator.
//
//   - held: each object, and whether the role holds the privilege
//     (has_any_column_privilege, has_sequence_privilege), predefined roles
//     such as pg_write_all_data included.
//   - acl, mine: every grant of the privilege on the object, table or
//     column; mine are those to PUBLIC or to a role the connecting role
//     inherits, which the held privilege comes from.
//   - paths: the owner's plain grants (kind 1); every other grant, named
//     with its grantee and grantor; a predefined role inherited without a
//     grant; each role it can SET ROLE to or administers (SET or MEMBER
//     WITH ADMIN OPTION from PostgreSQL 16, MEMBER before), whether it
//     inherits the role or not, that holds the privilege or is a
//     superuser (a non-superuser role it inherits is already covered by
//     held); CREATEROLE before PostgreSQL 16; and membership in
//     pg_execute_server_program or pg_write_server_files.
const collisionPrivilegesSQL = `
	WITH me AS (
	    SELECT r.oid, r.rolsuper, r.rolcreaterole,
	           pg_catalog.current_setting('server_version_num')::int >= 160000 AS pg16
	    FROM pg_catalog.pg_roles r WHERE r.rolname = current_user),
	held AS (
	    SELECT o.label, o.priv, o.kind, c.oid AS rel, c.relowner AS owner, c.relacl AS acl,
	           n.nspname AS schema_name, c.relname AS rel_name,
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
	    FROM held h CROSS JOIN me JOIN pg_catalog.pg_roles x ON x.oid <> me.oid
	    WHERE x.oid < 16384 AND NOT x.rolsuper AND pg_catalog.pg_has_role(me.oid, x.oid, 'USAGE')
	      AND CASE WHEN h.kind = 'TABLE' THEN pg_catalog.has_any_column_privilege(x.oid, h.rel, 'INSERT')
	               ELSE pg_catalog.has_sequence_privilege(x.oid, h.rel, 'USAGE') END
	      AND NOT EXISTS (SELECT 1 FROM acl a WHERE a.label = h.label AND a.grantee = x.oid)
	    UNION
	    SELECT h.label, 2, pg_catalog.format('%s%I, %s',
	               CASE WHEN NOT me.pg16 THEN 'membership in '
	                    WHEN pg_catalog.pg_has_role(me.oid, x.oid, 'SET') THEN 'SET ROLE to '
	                    ELSE 'ADMIN OPTION on ' END,
	               x.rolname::text, CASE WHEN x.rolsuper THEN 'a superuser' ELSE 'which holds ' || h.label END)
	    FROM held h CROSS JOIN me JOIN pg_catalog.pg_roles x ON x.oid <> me.oid
	    WHERE (x.rolsuper OR CASE WHEN h.kind = 'TABLE'
	                              THEN pg_catalog.has_any_column_privilege(x.oid, h.rel, 'INSERT')
	                              ELSE pg_catalog.has_sequence_privilege(x.oid, h.rel, 'USAGE') END)
	      AND (x.rolsuper OR NOT pg_catalog.pg_has_role(me.oid, x.oid, 'USAGE'))
	      AND CASE WHEN me.pg16
	               THEN pg_catalog.pg_has_role(me.oid, x.oid, 'SET')
	                    OR pg_catalog.pg_has_role(me.oid, x.oid, 'MEMBER WITH ADMIN OPTION')
	               ELSE pg_catalog.pg_has_role(me.oid, x.oid, 'MEMBER') END
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

// readCollisionPrivileges returns the privileges on sync_node_collisions
// and its sequence that the connecting role holds or can reach beyond the
// least-privilege list, the exact REVOKE statements the table owner runs
// for its plain grants, and every other path by name, sorted, for a role
// administrator (MTIX-95.1.7). A privilege with neither is named itself,
// so the fix is never empty. Each statement and path is listed once.
func readCollisionPrivileges(ctx context.Context, pool *transport.Pool) (held, revokes, paths []string, err error) {
	// Each privilege the role holds or reaches, with each REVOKE and each
	// named path (collisionPrivilegesSQL).
	rows, err := pool.Inner().Query(ctx, collisionPrivilegesSQL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read sync_node_collisions privileges: %w", err)
	}
	defer rows.Close()
	labels, texts, explained := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var label, text string
		var kind int
		if err := rows.Scan(&label, &kind, &text); err != nil {
			return nil, nil, nil, fmt.Errorf("read sync_node_collisions privileges: %w", err)
		}
		if !labels[label] {
			labels[label] = true
			held = append(held, label)
		}
		if text == "" {
			continue
		}
		explained[label] = true
		if texts[text] {
			continue
		}
		texts[text] = true
		if kind == 1 {
			revokes = append(revokes, text)
		} else {
			paths = append(paths, text)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("read sync_node_collisions privileges: %w", err)
	}
	for _, label := range held {
		if !explained[label] {
			paths = append(paths, unnamedPath(label))
		}
	}
	sort.Strings(paths)
	return held, revokes, paths, nil
}

// unnamedPath names a privilege the role holds by a path the check does
// not name (MTIX-95.1.7).
func unnamedPath(label string) string {
	return label + " held through a grant or a role membership"
}
