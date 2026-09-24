// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

// privKey is one privilege of one role on one object.
type privKey struct {
	obj       objKey
	role      uint32
	privilege string
}

// accessIndex answers whether a checked role's privilege is already
// explained, by a finding or by a kept role (MTIX-95.1).
type accessIndex struct {
	c           *hubCatalog
	acl         map[privKey]bool    // direct grants, PUBLIC included
	effective   map[privKey]bool    // privileges from any source
	findingRole map[uint32]bool     // grantees whose grants are findings
	edgeMember  map[uint32]bool     // checked direct members of a read-all role
	memberOf    map[uint32][]uint32 // checked role -> roles whose privileges it has
	kept        map[uint32]bool
}

// newAccessIndex indexes the catalog for residualFindings.
func newAccessIndex(c *hubCatalog, kept map[string]bool, checked map[uint32]bool) *accessIndex {
	x := &accessIndex{c: c, acl: map[privKey]bool{}, effective: map[privKey]bool{},
		findingRole: map[uint32]bool{}, edgeMember: map[uint32]bool{},
		memberOf: map[uint32][]uint32{}, kept: map[uint32]bool{}}
	for _, oid := range c.keptOIDs(kept) {
		x.kept[oid] = true
	}
	for _, e := range c.acl {
		x.acl[privKey{e.obj, e.grantee, e.privilege}] = true
		r := c.roles[e.grantee]
		if e.grantee != publicOID && !c.owners[e.grantee] && !r.super && e.grantee != c.current && !kept[r.name] {
			x.findingRole[e.grantee] = true
		}
	}
	for _, e := range c.effective {
		x.effective[privKey{e.obj, e.role, e.privilege}] = true
	}
	for _, e := range c.edges {
		if checked[e.member] {
			x.edgeMember[e.member] = true
		}
	}
	for pair := range c.usage {
		x.memberOf[pair[0]] = append(x.memberOf[pair[0]], pair[1])
	}
	return x
}

// explained reports whether e is covered: by a direct or PUBLIC grant
// (a grant finding), by membership of a kept role that holds it, by
// membership of an owner (an owner-membership finding), or by membership
// of a role whose grant or read-all membership is itself a finding.
func (x *accessIndex) explained(e effPriv) bool {
	if x.acl[privKey{e.obj, e.role, e.privilege}] || x.acl[privKey{e.obj, publicOID, e.privilege}] {
		return true
	}
	if x.edgeMember[e.role] {
		return true
	}
	for _, g := range x.memberOf[e.role] {
		switch {
		case x.kept[g] && x.effective[privKey{e.obj, g, e.privilege}]:
			return true
		case x.c.owners[g]:
			return true
		case x.findingRole[g] && x.acl[privKey{e.obj, g, e.privilege}]:
			return true
		case x.edgeMember[g]:
			return true
		}
	}
	return false
}

// residualFindings reports privileges a checked role holds on a sync
// object that nothing above explains: access through a membership that
// is not otherwise reported (FindingViaMembership). Such access has no
// fix here; it is remaining exposure an administrator resolves.
func residualFindings(c *hubCatalog, kept map[string]bool, checked map[uint32]bool) []finding {
	x := newAccessIndex(c, kept, checked)
	privs := map[grantKey][]string{}
	for _, e := range c.effective {
		if !checked[e.role] || x.explained(e) || c.object(e.obj) == nil {
			continue
		}
		k := grantKey{e.obj, e.role}
		privs[k] = append(privs[k], e.privilege)
	}
	out := make([]finding, 0, len(privs))
	for k, p := range privs {
		o := c.object(k.obj)
		out = append(out, finding{PrivilegeFinding: PrivilegeFinding{
			Role: c.roleName(k.grantee), Object: o.label(), Kind: o.kind,
			Privileges: sortedUnique(p), Via: FindingViaMembership,
		}})
	}
	return out
}
