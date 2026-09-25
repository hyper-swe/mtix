// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// collisionPrivilegesSQL reads, for INSERT on sync_node_collisions (on any
// of its columns included) and USAGE on its sequence, whether the
// connecting role holds the privilege, itself or through a role it
// inherits, or can reach it through SET ROLE to a role it does not inherit
// (SET from PostgreSQL 16, MEMBER before); the REVOKE of every grant it
// holds the privilege by (a grant to itself, to PUBLIC or to a role it
// inherits); and whether mtix sync harden is the fix instead: a grant made
// by a role other than the object's owner, which the owner's REVOKE does
// not remove, a grant option, which a REVOKE without CASCADE cannot remove
// once passed on, or a SET ROLE path (MTIX-95.1.7). A superuser and a role
// that is or inherits the owner are left out: ownership brings those
// privileges. Statements are built server-side by format() (SQL Rule 1a).
const collisionPrivilegesSQL = `
	WITH me AS (
	    SELECT r.oid, r.rolsuper,
	           CASE WHEN pg_catalog.current_setting('server_version_num')::int >= 160000
	                THEN 'SET' ELSE 'MEMBER' END AS set_mode
	    FROM pg_catalog.pg_roles r WHERE r.rolname = current_user),
	held AS (
	    SELECT o.label, o.priv, o.kind, c.oid AS rel, c.relowner AS owner, c.relacl AS acl,
	           n.nspname AS schema_name, c.relname AS rel_name,
	           CASE WHEN o.kind = 'TABLE' THEN pg_catalog.has_any_column_privilege(me.oid, c.oid, 'INSERT')
	                ELSE pg_catalog.has_sequence_privilege(me.oid, c.oid, 'USAGE') END AS direct,
	           EXISTS (SELECT 1 FROM pg_catalog.pg_roles r
	                   WHERE r.oid <> me.oid AND pg_catalog.pg_has_role(me.oid, r.oid, me.set_mode)
	                     AND NOT pg_catalog.pg_has_role(me.oid, r.oid, 'USAGE')
	                     AND CASE WHEN o.kind = 'TABLE'
	                              THEN pg_catalog.has_any_column_privilege(r.oid, c.oid, 'INSERT')
	                              ELSE pg_catalog.has_sequence_privilege(r.oid, c.oid, 'USAGE') END) AS by_set
	    FROM (VALUES
	          ('INSERT on sync_node_collisions', pg_catalog.to_regclass('sync_node_collisions'), 'INSERT', 'TABLE'),
	          ('USAGE on sync_node_collisions_collision_id_seq',
	           pg_catalog.to_regclass('sync_node_collisions_collision_id_seq'), 'USAGE', 'SEQUENCE'))
	         AS o(label, rel, priv, kind)
	    CROSS JOIN me
	    JOIN pg_catalog.pg_class c ON c.oid = o.rel
	    JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	    WHERE NOT me.rolsuper AND NOT pg_catalog.pg_has_role(me.oid, c.relowner, 'USAGE'))
	SELECT h.label,
	       COALESCE(pg_catalog.string_agg(DISTINCT g.stmt, ' ' ORDER BY g.stmt), ''),
	       h.by_set OR COALESCE(pg_catalog.bool_or(g.grantor <> h.owner OR g.is_grantable), false)
	FROM held h CROSS JOIN me
	LEFT JOIN LATERAL (
	    SELECT a.grantor, a.is_grantable,
	           pg_catalog.format('REVOKE %s ON %s %I.%I FROM %s;', h.priv, h.kind, h.schema_name, h.rel_name,
	               CASE WHEN a.grantee = 0 THEN 'PUBLIC'
	                    ELSE pg_catalog.quote_ident(pg_catalog.pg_get_userbyid(a.grantee)::text) END) AS stmt
	    FROM (SELECT x.grantee, x.grantor, x.is_grantable, x.privilege_type
	          FROM pg_catalog.aclexplode(h.acl) x
	          UNION ALL
	          SELECT x.grantee, x.grantor, x.is_grantable, x.privilege_type
	          FROM pg_catalog.pg_attribute att CROSS JOIN LATERAL pg_catalog.aclexplode(att.attacl) x
	          WHERE att.attrelid = h.rel AND att.attacl IS NOT NULL AND NOT att.attisdropped) a
	    WHERE a.privilege_type = h.priv
	      AND CASE WHEN a.grantee = 0 THEN true
	               ELSE pg_catalog.pg_has_role(me.oid, a.grantee, 'USAGE') END) g ON true
	WHERE h.direct OR h.by_set
	GROUP BY h.label, h.by_set
	ORDER BY 1`

// readCollisionPrivileges returns the privileges on sync_node_collisions
// and its sequence that the connecting role holds, or can reach through
// SET ROLE, beyond the least-privilege list; the REVOKE statements that
// remove those a REVOKE by the table owner clears; and whether any needs
// mtix sync harden instead, or has no statement to print (MTIX-95.1.7).
func readCollisionPrivileges(ctx context.Context, pool *transport.Pool) (held, revokes []string, harden bool, err error) {
	// Each privilege the role holds or reaches, the REVOKE of its grants,
	// and whether only mtix sync harden clears it (collisionPrivilegesSQL).
	rows, err := pool.Inner().Query(ctx, collisionPrivilegesSQL)
	if err != nil {
		return nil, nil, false, fmt.Errorf("read sync_node_collisions privileges: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var label, revoke string
		var needsHarden bool
		if err := rows.Scan(&label, &revoke, &needsHarden); err != nil {
			return nil, nil, false, fmt.Errorf("read sync_node_collisions privileges: %w", err)
		}
		held = append(held, label)
		if needsHarden || revoke == "" {
			harden = true
			continue
		}
		revokes = append(revokes, revoke)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("read sync_node_collisions privileges: %w", err)
	}
	return held, revokes, harden, nil
}
