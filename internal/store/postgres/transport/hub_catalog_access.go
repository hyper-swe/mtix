// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// checkedRoles returns the roles verification checks (MTIX-95.1): every
// role an administrator created, except superusers, the owners of the sync
// tables, the calling role and the kept roles. Predefined roles are not
// checked themselves; their members are, which is how membership in a
// read-all role is found.
func (c *hubCatalog) checkedRoles(kept map[string]bool) map[uint32]bool {
	out := map[uint32]bool{}
	for oid, r := range c.roles {
		if oid < firstNormalOID || r.super || c.owners[oid] || oid == c.current || kept[r.name] {
			continue
		}
		out[oid] = true
	}
	return out
}

// keptOIDs returns the OIDs of the kept roles.
func (c *hubCatalog) keptOIDs(kept map[string]bool) []uint32 {
	var out []uint32
	for oid, r := range c.roles {
		if kept[r.name] {
			out = append(out, oid)
		}
	}
	return out
}

// accessCandidates returns the roles whose privileges a checked role may
// hold through membership and that the attribution needs to know about:
// the owners, the kept roles, every grantee of a sync object, every member
// of a read-all role, and the read-all roles themselves.
func (c *hubCatalog) accessCandidates(kept map[string]bool) []uint32 {
	set := map[uint32]bool{}
	for oid := range c.owners {
		set[oid] = true
	}
	for _, oid := range c.keptOIDs(kept) {
		set[oid] = true
	}
	for _, e := range c.acl {
		if e.grantee != publicOID {
			set[e.grantee] = true
		}
	}
	for _, e := range c.edges {
		set[e.role], set[e.member] = true, true
	}
	out := make([]uint32, 0, len(set))
	for oid := range set {
		out = append(out, oid)
	}
	return out
}

// loadAccess reads which candidate roles each checked role has the
// privileges of, and every privilege the checked and kept roles hold on
// the sync objects however they got it: has_table_privilege and its
// sequence and function forms see grants, PUBLIC and inherited membership
// alike, which role_table_grants does not (F-19).
func (c *hubCatalog) loadAccess(ctx context.Context, tx pgx.Tx, kept []string) error {
	keptSet := map[string]bool{}
	for _, k := range kept {
		keptSet[k] = true
	}
	checked := make([]uint32, 0)
	for oid := range c.checkedRoles(keptSet) {
		checked = append(checked, oid)
	}
	if err := c.loadUsage(ctx, tx, checked, c.accessCandidates(keptSet)); err != nil {
		return err
	}
	return c.loadEffective(ctx, tx, append(checked, c.keptOIDs(keptSet)...))
}

// loadUsage records, for each (checked role, candidate) pair, whether the
// checked role inherits the candidate's privileges (usage) and whether it
// can use them at all, by inheriting or by SET ROLE (member). Before
// PostgreSQL 16 every member can SET ROLE, so MEMBER stands for SET there.
func (c *hubCatalog) loadUsage(ctx context.Context, tx pgx.Tx, checked, candidates []uint32) error {
	// Which candidate roles each checked role inherits, or can SET ROLE to.
	rows, err := tx.Query(ctx, `
		SELECT r.oid, g.oid, COALESCE(pg_catalog.pg_has_role(r.oid, g.oid, 'USAGE'), false)
		FROM unnest($1::oid[]) AS r(oid) CROSS JOIN unnest($2::oid[]) AS g(oid)
		WHERE r.oid <> g.oid
		  AND COALESCE(pg_catalog.pg_has_role(r.oid, g.oid, 'USAGE')
		       OR pg_catalog.pg_has_role(r.oid, g.oid,
		            CASE WHEN current_setting('server_version_num')::int >= 160000
		                 THEN 'SET' ELSE 'MEMBER' END), false)`,
		checked, candidates)
	if err != nil {
		return fmt.Errorf("read role memberships: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pair [2]uint32
		var inherits bool
		if err := rows.Scan(&pair[0], &pair[1], &inherits); err != nil {
			return fmt.Errorf("read role memberships: %w", err)
		}
		c.member[pair] = true
		if inherits {
			c.usage[pair] = true
		}
	}
	return rows.Err()
}

// loadEffective records every privilege the given roles hold on the sync
// tables (the seven table privileges; SELECT, INSERT, UPDATE and
// REFERENCES also when held on any single column), sequences (USAGE,
// SELECT, UPDATE) and mtix functions (EXECUTE).
func (c *hubCatalog) loadEffective(ctx context.Context, tx pgx.Tx, roles []uint32) error {
	// Every privilege each role holds on each sync object, from any source.
	rows, err := tx.Query(ctx, `
		SELECT r.oid, 'r', o.oid, p.priv
		FROM unnest($1::oid[]) AS r(oid) CROSS JOIN unnest($2::oid[]) AS o(oid)
		CROSS JOIN unnest(ARRAY['SELECT','INSERT','UPDATE','DELETE','TRUNCATE','REFERENCES','TRIGGER']) AS p(priv)
		WHERE COALESCE(pg_catalog.has_table_privilege(r.oid, o.oid, p.priv), false)
		   OR (p.priv IN ('SELECT', 'INSERT', 'UPDATE', 'REFERENCES')
		       AND COALESCE(pg_catalog.has_any_column_privilege(r.oid, o.oid, p.priv), false))
		UNION ALL
		SELECT r.oid, 'r', o.oid, p.priv
		FROM unnest($1::oid[]) AS r(oid) CROSS JOIN unnest($3::oid[]) AS o(oid)
		CROSS JOIN unnest(ARRAY['USAGE','SELECT','UPDATE']) AS p(priv)
		WHERE COALESCE(pg_catalog.has_sequence_privilege(r.oid, o.oid, p.priv), false)
		UNION ALL
		SELECT r.oid, 'f', o.oid, 'EXECUTE'
		FROM unnest($1::oid[]) AS r(oid) CROSS JOIN unnest($4::oid[]) AS o(oid)
		WHERE COALESCE(pg_catalog.has_function_privilege(r.oid, o.oid, 'EXECUTE'), false)`,
		roles, c.oidsOf(FindingKindTable), c.oidsOf(FindingKindSequence), c.oidsOf(FindingKindFunction))
	if err != nil {
		return fmt.Errorf("read effective privileges: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cat string
		var e effPriv
		if err := rows.Scan(&e.role, &cat, &e.obj.oid, &e.privilege); err != nil {
			return fmt.Errorf("read effective privileges: %w", err)
		}
		e.obj.cat = cat[0]
		c.effective = append(c.effective, e)
	}
	return rows.Err()
}
