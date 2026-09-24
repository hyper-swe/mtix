// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

// regrantKey identifies one privilege of one kept role on one object.
type regrantKey struct {
	obj       objKey
	grantee   uint32
	privilege string
	column    string
}

// regrantFindings reports each privilege on a sync table or sequence that a
// kept role holds by the grant of a role other than the owners (MTIX-95.1).
// --apply revokes every other role's privileges, and a kept role's grant
// options, with CASCADE, which would also remove what that role granted to
// the kept role. So the owner grants the privilege again first, and the
// kept role keeps it. The mtix functions are trigger functions, whose
// EXECUTE is not needed, so they are left out; so is an object another role
// owns, which the owner cannot grant on (it is an object_owner finding).
func regrantFindings(c *hubCatalog, kept map[string]bool) []finding {
	seen := map[regrantKey]bool{}
	var out []finding
	for _, e := range c.acl {
		o := c.object(e.obj)
		r := c.roles[e.grantee]
		if o == nil || o.kind == FindingKindFunction || !c.owners[o.owner] || e.grantee == publicOID ||
			!kept[r.name] || c.owners[e.grantor] {
			continue
		}
		k := regrantKey{e.obj, e.grantee, e.privilege, e.column}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, regrantFinding(c, o, e, r.name))
	}
	return out
}

// regrantFinding builds the finding and the owner's GRANT for one kept
// privilege.
func regrantFinding(c *hubCatalog, o *hubObject, e aclEntry, role string) finding {
	label := e.privilege
	if e.column != "" {
		label += " (" + e.column + ")"
	}
	f := finding{PrivilegeFinding: PrivilegeFinding{
		Role: role, Object: o.label(), Kind: o.kind, Privileges: []string{label}, Via: FindingViaGrantor,
		Note: "granted by " + c.roleName(e.grantor) + "; --apply grants it again from the owner, " +
			"so the kept role keeps it when that role's grants are revoked",
	}}
	keyword := "TABLE"
	if o.kind == FindingKindSequence {
		keyword = "SEQUENCE"
	}
	if e.column != "" {
		f.fix = newAction("regrant-column", sqlFmtGrantColumn, rankRegrant, e.privilege, e.column, o.schema, o.name, role)
	} else {
		f.fix = newAction("regrant", sqlFmtGrantRelation, rankRegrant, e.privilege, keyword, o.schema, o.name, role)
	}
	if f.fix == nil {
		f.Note += "; " + noteBadName
	}
	return f
}

// createRoleFindings reports checked CREATEROLE roles on a server before
// PostgreSQL 16, where CREATEROLE can grant membership in any role that is
// not a superuser, the owner and the read-all roles included. From 16 on,
// such a role reaches only roles it holds ADMIN on, which the membership
// checks cover. mtix never changes role attributes.
func createRoleFindings(c *hubCatalog, checked map[uint32]bool) []finding {
	if c.serverVersion >= 160000 {
		return nil
	}
	var out []finding
	for r := range checked {
		if !c.roles[r].createRole {
			continue
		}
		out = append(out, finding{PrivilegeFinding: PrivilegeFinding{
			Role: c.roleName(r), Object: "CREATEROLE", Kind: FindingKindRole, Via: FindingViaCreateRole,
			Note: "before PostgreSQL 16 a CREATEROLE role can grant itself membership in any role that " +
				"is not a superuser, the owner included; an administrator removes CREATEROLE",
		}})
	}
	return out
}
