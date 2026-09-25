// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
)

// oidsOf returns the OIDs of the loaded objects of kind.
func (c *hubCatalog) oidsOf(kind string) []uint32 {
	var out []uint32
	for _, o := range c.objects {
		if o.kind == kind {
			out = append(out, o.key.oid)
		}
	}
	return out
}

// loadSequences finds the sequences the sync tables use: those owned by a
// sync table column (serial and identity columns) and those a column
// default draws from, even after OWNED BY NONE (MTIX-95.1).
func (c *hubCatalog) loadSequences(ctx context.Context, tx pgx.Tx) error {
	// Sequences owned by a sync table, plus sequences its column defaults use.
	rows, err := tx.Query(ctx, `
		SELECT s.oid, n.nspname::text, s.relname::text, s.relowner, false
		FROM pg_catalog.pg_depend d
		JOIN pg_catalog.pg_class s ON s.oid = d.objid AND s.relkind = 'S'
		JOIN pg_catalog.pg_namespace n ON n.oid = s.relnamespace
		WHERE d.classid = 'pg_catalog.pg_class'::regclass
		  AND d.refclassid = 'pg_catalog.pg_class'::regclass
		  AND d.refobjid = ANY($1::oid[]) AND d.deptype IN ('a', 'i')
		UNION
		SELECT s.oid, n.nspname::text, s.relname::text, s.relowner, false
		FROM pg_catalog.pg_attrdef ad
		JOIN pg_catalog.pg_depend d ON d.classid = 'pg_catalog.pg_attrdef'::regclass
		     AND d.objid = ad.oid AND d.refclassid = 'pg_catalog.pg_class'::regclass
		JOIN pg_catalog.pg_class s ON s.oid = d.refobjid AND s.relkind = 'S'
		JOIN pg_catalog.pg_namespace n ON n.oid = s.relnamespace
		WHERE ad.adrelid = ANY($1::oid[])
		ORDER BY 3`, c.oidsOf(FindingKindTable))
	if err != nil {
		return fmt.Errorf("read sync sequences: %w", err)
	}
	return c.appendObjects(rows, catRelation, FindingKindSequence)
}

// loadFunctions finds the mtix functions, by the names the migrations
// define, in the sync schema, and whether each returns trigger. Each takes
// no arguments.
func (c *hubCatalog) loadFunctions(ctx context.Context, tx pgx.Tx) error {
	names, err := migrations.Functions()
	if err != nil {
		return fmt.Errorf("mtix function list: %w", err)
	}
	// The zero-argument mtix functions in the sync schema.
	rows, err := tx.Query(ctx, `
		SELECT p.oid, n.nspname::text, p.proname::text, p.proowner,
		       p.prorettype = 'pg_catalog.trigger'::pg_catalog.regtype
		FROM pg_catalog.pg_proc p
		JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
		WHERE p.proname = ANY($1::text[]) AND p.pronargs = 0 AND n.nspname = $2
		ORDER BY 3`, names, c.schema)
	if err != nil {
		return fmt.Errorf("read mtix functions: %w", err)
	}
	return c.appendObjects(rows, catFunction, FindingKindFunction)
}

// appendObjects scans (oid, schema, name, owner, returns trigger) rows
// into c.objects.
func (c *hubCatalog) appendObjects(rows pgx.Rows, cat byte, kind string) error {
	defer rows.Close()
	for rows.Next() {
		o := hubObject{key: objKey{cat: cat}, kind: kind}
		if err := rows.Scan(&o.key.oid, &o.schema, &o.name, &o.owner, &o.trigger); err != nil {
			return fmt.Errorf("read %s: %w", kind, err)
		}
		c.objects = append(c.objects, o)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read %s: %w", kind, err)
	}
	return nil
}

// loadACL reads every privilege on the sync objects, including privileges
// granted on single columns of a sync table. A NULL ACL stands for the
// built-in defaults, which acldefault spells out: on a function they
// include EXECUTE for PUBLIC.
func (c *hubCatalog) loadACL(ctx context.Context, tx pgx.Tx) error {
	tables := c.oidsOf(FindingKindTable)
	relations := append(append([]uint32{}, tables...), c.oidsOf(FindingKindSequence)...)
	// Explode the ACL of each sync table, sequence, mtix function and of
	// each sync table column that has its own ACL.
	rows, err := tx.Query(ctx, `
		SELECT 'r', c.oid, a.grantee, a.grantor, a.privilege_type, a.is_grantable, ''
		FROM pg_catalog.pg_class c
		CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(c.relacl, pg_catalog.acldefault(
		     CASE WHEN c.relkind = 'S' THEN 's' ELSE 'r' END::"char", c.relowner))) a
		WHERE c.oid = ANY($1::oid[])
		UNION ALL
		SELECT 'r', att.attrelid, a.grantee, a.grantor, a.privilege_type, a.is_grantable, att.attname::text
		FROM pg_catalog.pg_attribute att
		CROSS JOIN LATERAL pg_catalog.aclexplode(att.attacl) a
		WHERE att.attrelid = ANY($3::oid[]) AND att.attacl IS NOT NULL AND NOT att.attisdropped
		UNION ALL
		SELECT 'f', p.oid, a.grantee, a.grantor, a.privilege_type, a.is_grantable, ''
		FROM pg_catalog.pg_proc p
		CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(p.proacl,
		     pg_catalog.acldefault('f', p.proowner))) a
		WHERE p.oid = ANY($2::oid[])`, relations, c.oidsOf(FindingKindFunction), tables)
	if err != nil {
		return fmt.Errorf("read sync privileges: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cat string
		var e aclEntry
		if err := rows.Scan(&cat, &e.obj.oid, &e.grantee, &e.grantor, &e.privilege, &e.grantable, &e.column); err != nil {
			return fmt.Errorf("read sync privileges: %w", err)
		}
		e.obj.cat = cat[0]
		c.acl = append(c.acl, e)
	}
	return rows.Err()
}

// loadDefaults reads the default privileges for tables, sequences and
// functions that apply in the sync schema: its own entries and the entries
// that apply in every schema.
func (c *hubCatalog) loadDefaults(ctx context.Context, tx pgx.Tx) error {
	// Default privileges in the sync schema or in all schemas (namespace 0).
	rows, err := tx.Query(ctx, `
		SELECT d.defaclrole, COALESCE(n.nspname::text, ''), d.defaclobjtype::text,
		       a.grantee, a.privilege_type
		FROM pg_catalog.pg_default_acl d
		LEFT JOIN pg_catalog.pg_namespace n ON n.oid = d.defaclnamespace
		CROSS JOIN LATERAL pg_catalog.aclexplode(d.defaclacl) a
		WHERE d.defaclobjtype IN ('r', 'S', 'f')
		  AND (d.defaclnamespace = 0 OR n.nspname = $1)`, c.schema)
	if err != nil {
		return fmt.Errorf("read default privileges: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d defaultACL
		if err := rows.Scan(&d.creator, &d.schema, &d.objType, &d.grantee, &d.privilege); err != nil {
			return fmt.Errorf("read default privileges: %w", err)
		}
		c.defaults = append(c.defaults, d)
	}
	return rows.Err()
}

// loadEdges reads every direct member of the read-all predefined roles,
// whatever the membership carries: inheriting, SET ROLE, or only ADMIN
// OPTION, with which a member can grant the role to itself. It also reads
// whether the caller may revoke each membership: that needs ADMIN on the
// role and the privileges of the role that granted it.
func (c *hubCatalog) loadEdges(ctx context.Context, tx pgx.Tx) error {
	// Direct memberships in pg_read_all_data, pg_write_all_data, pg_maintain.
	rows, err := tx.Query(ctx, `
		SELECT m.roleid, m.member, m.grantor,
		       COALESCE(pg_catalog.pg_has_role(current_user, m.roleid, 'USAGE WITH ADMIN OPTION')
		            AND pg_catalog.pg_has_role(current_user, m.grantor, 'USAGE'), false)
		FROM pg_catalog.pg_auth_members m
		JOIN pg_catalog.pg_roles r ON r.oid = m.roleid
		WHERE r.rolname = ANY($1::text[])`, readAllRoles)
	if err != nil {
		return fmt.Errorf("read role memberships: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e roleEdge
		if err := rows.Scan(&e.role, &e.member, &e.grantor, &e.revocable); err != nil {
			return fmt.Errorf("read role memberships: %w", err)
		}
		c.edges = append(c.edges, e)
	}
	return rows.Err()
}

// loadGuards reads the state of each TRUNCATE guard the migrations define.
func (c *hubCatalog) loadGuards(ctx context.Context, tx pgx.Tx) error {
	guards, err := migrations.TruncateGuards()
	if err != nil {
		return fmt.Errorf("guard list: %w", err)
	}
	tables := make([]string, 0, len(guards))
	names := make([]string, 0, len(guards))
	for _, g := range guards {
		tables, names = append(tables, g.Table), append(names, g.Name)
	}
	// Each guard's enabled state, the function it calls (schema-qualified)
	// and whether that is the guard function of the table's schema by OID,
	// so a function of the same name in another schema does not count
	// (MTIX-95.7); empty and false when the guard is missing.
	rows, err := tx.Query(ctx, `
		SELECT g.tbl, g.name, COALESCE(t.tgenabled::text, ''),
		       COALESCE(pn.nspname::text || '.' || p.proname::text, ''),
		       COALESCE(t.tgfoid = pg_catalog.to_regprocedure(
		                pg_catalog.quote_ident(n.nspname) || '.' || pg_catalog.quote_ident($3) || '()'), false)
		FROM unnest($1::text[], $2::text[]) AS g(tbl, name)
		LEFT JOIN pg_catalog.pg_class c ON c.oid = pg_catalog.to_regclass(g.tbl)
		LEFT JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_catalog.pg_trigger t
		       ON t.tgrelid = c.oid AND t.tgname = g.name AND NOT t.tgisinternal
		LEFT JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
		LEFT JOIN pg_catalog.pg_namespace pn ON pn.oid = p.pronamespace
		ORDER BY 1`, tables, names, guardFunction)
	if err != nil {
		return fmt.Errorf("read guard triggers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var g guardState
		if err := rows.Scan(&g.table, &g.trigger, &g.enabled, &g.function, &g.bound); err != nil {
			return fmt.Errorf("read guard triggers: %w", err)
		}
		c.guards = append(c.guards, g)
	}
	return rows.Err()
}
