// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// collisionPrivilegesSQL reads how the connecting role holds or can reach
// INSERT on sync_node_collisions (on any of its columns included) and
// USAGE on its sequence, and the statements that remove each path
// (MTIX-95.1.7). A superuser and a role that is or inherits the object's
// owner are left out: ownership brings those privileges. The statements
// are built server-side by format() (SQL Rule 1a); runner 1 is the table
// owner, runner 2 a role administrator.
//
//   - held: each object, and whether the role holds the privilege itself
//     or through a role it inherits.
//   - acl, mine: every grant of the privilege on the object, table or
//     column; mine are those to PUBLIC or to a role the connecting role
//     inherits.
//   - chain: for a grant another role made, the grants up to the one the
//     owner made (with grant option).
//   - reach: roles through which the privilege is reached by a membership
//     rather than a grant: a role the connecting role does not inherit but
//     can SET ROLE to or administer (SET or MEMBER WITH ADMIN OPTION from
//     PostgreSQL 16, MEMBER before), and a predefined role it inherits
//     that holds the privilege without a grant, such as pg_write_all_data.
//   - fixes: the owner's REVOKE of each grant it made (CASCADE for a grant
//     option), its REVOKE GRANT OPTION ... CASCADE from the role it granted
//     a chain's first grant to, and the administrator's REVOKE of each
//     direct membership of the connecting role that leads to a reach role
//     (GRANTED BY its grantor from PostgreSQL 16).
const collisionPrivilegesSQL = `
	WITH RECURSIVE me AS (
	    SELECT r.oid, r.rolsuper, pg_catalog.current_setting('server_version_num')::int >= 160000 AS pg16
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
	    SELECT a.label, a.grantee, a.grantor, a.is_grantable FROM acl a CROSS JOIN me
	    WHERE CASE WHEN a.grantee = 0 THEN true ELSE pg_catalog.pg_has_role(me.oid, a.grantee, 'USAGE') END),
	chain(label, grantee, grantor, depth) AS (
	    SELECT m.label, m.grantee, m.grantor, 0 FROM mine m
	    UNION
	    SELECT c.label, a.grantee, a.grantor, c.depth + 1
	    FROM chain c JOIN held h ON h.label = c.label
	    JOIN acl a ON a.label = c.label AND a.grantee = c.grantor AND a.is_grantable
	    WHERE c.grantor <> h.owner AND c.depth < 8),
	reach AS (
	    SELECT h.label, x.oid AS role
	    FROM held h CROSS JOIN me JOIN pg_catalog.pg_roles x ON x.oid <> me.oid
	    WHERE CASE WHEN h.kind = 'TABLE' THEN pg_catalog.has_any_column_privilege(x.oid, h.rel, 'INSERT')
	               ELSE pg_catalog.has_sequence_privilege(x.oid, h.rel, 'USAGE') END
	      AND CASE WHEN pg_catalog.pg_has_role(me.oid, x.oid, 'USAGE')
	               THEN x.oid < 16384 AND NOT x.rolsuper
	                    AND NOT EXISTS (SELECT 1 FROM acl a WHERE a.label = h.label AND a.grantee = x.oid)
	               WHEN me.pg16
	               THEN pg_catalog.pg_has_role(me.oid, x.oid, 'SET')
	                    OR pg_catalog.pg_has_role(me.oid, x.oid, 'MEMBER WITH ADMIN OPTION')
	               ELSE pg_catalog.pg_has_role(me.oid, x.oid, 'MEMBER') END),
	fixes AS (
	    SELECT m.label, 1 AS runner,
	           pg_catalog.format('REVOKE %s ON %s %I.%I FROM %s%s;', h.priv, h.kind, h.schema_name, h.rel_name,
	               CASE WHEN m.grantee = 0 THEN 'PUBLIC'
	                    ELSE pg_catalog.quote_ident(pg_catalog.pg_get_userbyid(m.grantee)::text) END,
	               CASE WHEN m.is_grantable THEN ' CASCADE' ELSE '' END) AS stmt
	    FROM mine m JOIN held h ON h.label = m.label WHERE m.grantor = h.owner
	    UNION
	    SELECT c.label, 1, pg_catalog.format('REVOKE GRANT OPTION FOR %s ON %s %I.%I FROM %I CASCADE;',
	               h.priv, h.kind, h.schema_name, h.rel_name, pg_catalog.pg_get_userbyid(c.grantee)::text)
	    FROM chain c JOIN held h ON h.label = c.label WHERE c.depth > 0 AND c.grantor = h.owner
	    UNION
	    SELECT r.label, 2,
	           CASE WHEN me.pg16
	                THEN pg_catalog.format('REVOKE %I FROM %I GRANTED BY %I;',
	                         pg_catalog.pg_get_userbyid(m.roleid)::text, pg_catalog.pg_get_userbyid(me.oid)::text,
	                         pg_catalog.pg_get_userbyid(m.grantor)::text)
	                ELSE pg_catalog.format('REVOKE %I FROM %I;',
	                         pg_catalog.pg_get_userbyid(m.roleid)::text, pg_catalog.pg_get_userbyid(me.oid)::text) END
	    FROM reach r CROSS JOIN me JOIN pg_catalog.pg_auth_members m ON m.member = me.oid
	    WHERE m.roleid = r.role OR pg_catalog.pg_has_role(m.roleid, r.role, 'MEMBER'))
	SELECT h.label, COALESCE(f.runner, 0), COALESCE(f.stmt, '')
	FROM held h LEFT JOIN fixes f ON f.label = h.label
	WHERE h.direct OR EXISTS (SELECT 1 FROM reach r WHERE r.label = h.label)
	ORDER BY 1, 2, 3`

// readCollisionPrivileges returns the privileges on sync_node_collisions
// and its sequence that the connecting role holds, or can reach through a
// role membership, beyond the least-privilege list; the REVOKE statements
// the table owner runs for the grants; and the membership REVOKE
// statements a role administrator runs. Each statement is listed once,
// even when it clears both privileges (MTIX-95.1.7).
func readCollisionPrivileges(ctx context.Context, pool *transport.Pool) (held, revokes, memberships []string, err error) {
	// Each privilege the role holds or reaches, with each statement that
	// removes a path to it (collisionPrivilegesSQL).
	rows, err := pool.Inner().Query(ctx, collisionPrivilegesSQL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read sync_node_collisions privileges: %w", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var label, stmt string
		var runner int
		if err := rows.Scan(&label, &runner, &stmt); err != nil {
			return nil, nil, nil, fmt.Errorf("read sync_node_collisions privileges: %w", err)
		}
		if !seen[label] {
			seen[label] = true
			held = append(held, label)
		}
		if stmt == "" || seen[stmt] {
			continue
		}
		seen[stmt] = true
		if runner == 1 {
			revokes = append(revokes, stmt)
		} else {
			memberships = append(memberships, stmt)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("read sync_node_collisions privileges: %w", err)
	}
	return held, revokes, memberships, nil
}
