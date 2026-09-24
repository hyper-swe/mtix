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
// tables and the kept roles. The connecting role is checked like any other
// unless it is one of those. Predefined roles are not checked themselves;
// their members are, which is how membership in a read-all role is found.
func (c *hubCatalog) checkedRoles(kept map[string]bool) map[uint32]bool {
	out := map[uint32]bool{}
	for oid, r := range c.roles {
		if oid < firstNormalOID || r.super || c.owners[oid] || kept[r.name] {
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
// of a read-all role, the read-all roles themselves, and the roles through
// which a member reaches everything: superusers and the server-file and
// server-program roles, and every role that can SET ROLE to a superuser,
// which a holder of ADMIN OPTION on it can grant to itself (MTIX-95.1).
func (c *hubCatalog) accessCandidates(kept map[string]bool) []uint32 {
	set := map[uint32]bool{}
	for oid := range c.owners {
		set[oid] = true
	}
	for oid, r := range c.roles {
		if r.super || escalationRoles[r.name] {
			set[oid] = true
		}
	}
	for oid := range c.superReach {
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
	if err := c.loadSuperReach(ctx, tx); err != nil {
		return err
	}
	if err := c.loadUsage(ctx, tx, checked, c.accessCandidates(keptSet)); err != nil {
		return err
	}
	return c.loadEffective(ctx, tx, append(checked, c.keptOIDs(keptSet)...))
}

// loadUsage records, for each (checked role, candidate) pair, whether the
// checked role is a member at all (member: by inheriting, by SET ROLE, or
// by ADMIN OPTION alone, with which it can grant the candidate to itself),
// inherits the candidate's privileges (usage), can SET ROLE to it (canSet;
// MEMBER before PostgreSQL 16, where every member can), and holds ADMIN
// OPTION on it, directly or through any membership (admin). MEMBER counts
// every kind of membership on every version.
func (c *hubCatalog) loadUsage(ctx context.Context, tx pgx.Tx, checked, candidates []uint32) error {
	// Which candidate roles each checked role is a member of, inherits, can
	// SET ROLE to, or can grant.
	rows, err := tx.Query(ctx, `
		SELECT r.oid, g.oid,
		       COALESCE(pg_catalog.pg_has_role(r.oid, g.oid, 'MEMBER'), false),
		       COALESCE(pg_catalog.pg_has_role(r.oid, g.oid, 'USAGE'), false),
		       COALESCE(pg_catalog.pg_has_role(r.oid, g.oid,
		            CASE WHEN current_setting('server_version_num')::int >= 160000
		                 THEN 'SET' ELSE 'MEMBER' END), false),
		       COALESCE(pg_catalog.pg_has_role(r.oid, g.oid, 'USAGE WITH ADMIN OPTION'), false)
		FROM unnest($1::oid[]) AS r(oid) CROSS JOIN unnest($2::oid[]) AS g(oid)
		WHERE r.oid <> g.oid
		  AND (COALESCE(pg_catalog.pg_has_role(r.oid, g.oid, 'MEMBER'), false)
		       OR COALESCE(pg_catalog.pg_has_role(r.oid, g.oid, 'USAGE WITH ADMIN OPTION'), false))`,
		checked, candidates)
	if err != nil {
		return fmt.Errorf("read role memberships: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pair [2]uint32
		var member, inherits, canSet, admin bool
		if err := rows.Scan(&pair[0], &pair[1], &member, &inherits, &canSet, &admin); err != nil {
			return fmt.Errorf("read role memberships: %w", err)
		}
		c.recordMembership(pair, member, inherits, canSet, admin)
	}
	return rows.Err()
}

// recordMembership stores the true facts about one membership pair.
func (c *hubCatalog) recordMembership(pair [2]uint32, member, inherits, canSet, admin bool) {
	for _, fact := range []struct {
		set map[[2]uint32]bool
		ok  bool
	}{{c.member, member}, {c.usage, inherits}, {c.canSet, canSet}, {c.admin, admin}} {
		if fact.ok {
			fact.set[pair] = true
		}
	}
}

// loadSuperReach records every role that is not a superuser but can SET
// ROLE to one (MEMBER before PostgreSQL 16), with the superusers it can
// reach (MTIX-95.1). A role holding ADMIN OPTION on such a role can grant
// it to itself with SET and so act as the superuser.
func (c *hubCatalog) loadSuperReach(ctx context.Context, tx pgx.Tx) error {
	// Non-superuser roles that can SET ROLE to a superuser.
	rows, err := tx.Query(ctx, `
		SELECT y.oid, s.oid
		FROM pg_catalog.pg_roles y CROSS JOIN pg_catalog.pg_roles s
		WHERE s.rolsuper AND NOT y.rolsuper AND y.oid <> s.oid
		  AND COALESCE(pg_catalog.pg_has_role(y.oid, s.oid,
		       CASE WHEN current_setting('server_version_num')::int >= 160000
		            THEN 'SET' ELSE 'MEMBER' END), false)`)
	if err != nil {
		return fmt.Errorf("read superuser memberships: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var y, s uint32
		if err := rows.Scan(&y, &s); err != nil {
			return fmt.Errorf("read superuser memberships: %w", err)
		}
		c.superReach[y] = append(c.superReach[y], s)
	}
	return rows.Err()
}

// loadEffective records every privilege the given roles hold on the sync
// tables (the seven table privileges, and MAINTAIN from PostgreSQL 17;
// SELECT, INSERT, UPDATE and REFERENCES also when held on any single
// column), sequences (USAGE, SELECT, UPDATE) and mtix functions (EXECUTE).
func (c *hubCatalog) loadEffective(ctx context.Context, tx pgx.Tx, roles []uint32) error {
	// Every privilege each role holds on each sync object, from any source.
	rows, err := tx.Query(ctx, `
		SELECT r.oid, 'r', o.oid, p.priv
		FROM unnest($1::oid[]) AS r(oid) CROSS JOIN unnest($2::oid[]) AS o(oid)
		CROSS JOIN unnest(ARRAY['SELECT','INSERT','UPDATE','DELETE','TRUNCATE','REFERENCES','TRIGGER']
		     || CASE WHEN current_setting('server_version_num')::int >= 170000
		             THEN ARRAY['MAINTAIN'] ELSE ARRAY[]::text[] END) AS p(priv)
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
