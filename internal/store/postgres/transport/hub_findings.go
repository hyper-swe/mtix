// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"sort"
	"strings"
)

// finding is a PrivilegeFinding with the action that fixes it, or the
// statement an administrator runs when the caller cannot.
type finding struct {
	PrivilegeFinding
	fix    *action
	manual *action
}

// Notes that explain a finding or information item.
const (
	// noteBadName explains exposure whose names mtix will not put in a
	// statement (SQL Rule 1a).
	noteBadName = "a name is outside the identifier form mtix puts in statements; revoke it by hand"

	// noteTriggerFunction explains why EXECUTE on an mtix trigger function
	// is information, not a finding.
	noteTriggerFunction = "a trigger function cannot be called directly and triggers fire without EXECUTE; " +
		"--apply revokes it when it makes other changes"

	// noteOtherCreator explains why another role's default privileges are
	// information, not a finding.
	noteOtherCreator = "another role's default privileges do not apply to tables the owner creates"

	// noteObjectOwner explains an mtix object owned by another role.
	noteObjectOwner = "owned by a role other than the sync tables' owner, which can replace or alter it; " +
		"an administrator returns it to the owner"
)

// computeFindings verifies the catalog against the kept roles (MTIX-95.1)
// and returns the findings, which fail verification, and the information
// items, which do not. Only the owners, superusers, the caller and the
// kept roles may hold access; a kept role may not pass its access on.
// Information items are EXECUTE on the mtix trigger functions and other
// roles' default privileges.
func computeFindings(c *hubCatalog, kept map[string]bool) (findings, info []finding) {
	checked := c.checkedRoles(kept)
	grants, grantInfo := grantFindings(c, kept)
	findings = append(findings, grants...)
	defaults, other := defaultFindings(c, kept)
	findings = append(findings, defaults...)
	findings = append(findings, edgeFindings(c, checked)...)
	findings = append(findings, ownerMemberFindings(c, checked)...)
	findings = append(findings, residualFindings(c, kept, checked)...)
	findings = append(findings, guardFindings(c)...)
	findings = append(findings, objectOwnerFindings(c)...)
	info = append(info, grantInfo...)
	info = append(info, other...)
	sortFindings(findings)
	sortFindings(info)
	return findings, info
}

// grantKey groups ACL entries by object and grantee.
type grantKey struct {
	obj     objKey
	grantee uint32
}

// grantFindings reports every privilege on a sync object, or on one of its
// columns, held by PUBLIC or a non-kept role, and every grant option a kept
// role holds. Those on an mtix trigger function are information.
func grantFindings(c *hubCatalog, kept map[string]bool) (findings, info []finding) {
	privs := map[grantKey][]aclEntry{}
	options := map[grantKey][]aclEntry{}
	for _, e := range c.acl {
		if c.object(e.obj) == nil {
			continue
		}
		k := grantKey{e.obj, e.grantee}
		r := c.roles[e.grantee]
		switch {
		case e.grantee == publicOID:
			privs[k] = append(privs[k], e)
		case c.owners[e.grantee] || r.super || e.grantee == c.current:
		case kept[r.name]:
			if e.grantable {
				options[k] = append(options[k], e)
			}
		default:
			privs[k] = append(privs[k], e)
		}
	}
	for _, group := range []struct {
		entries map[grantKey][]aclEntry
		via     string
	}{{privs, FindingViaGrant}, {options, FindingViaGrantOption}} {
		for k, es := range group.entries {
			f := grantFinding(c, k, es, group.via)
			if c.object(k.obj).trigger {
				f.Note = strings.TrimSuffix(noteTriggerFunction+"; "+f.Note, "; ")
				info = append(info, f)
				continue
			}
			findings = append(findings, f)
		}
	}
	return findings, info
}

// grantFinding builds the finding, and its revoke, for one grantee on one
// object. REVOKE ALL on a table also removes its column privileges.
func grantFinding(c *hubCatalog, k grantKey, es []aclEntry, via string) finding {
	o := c.object(k.obj)
	role := c.roleName(k.grantee)
	f := finding{PrivilegeFinding: PrivilegeFinding{
		Role: role, Object: o.label(), Kind: o.kind, Privileges: privilegeLabels(es), Via: via,
	}}
	f.fix = revokeAction(o, k.grantee, role, via)
	if f.fix == nil {
		f.Note = noteBadName
	}
	return f
}

// privilegeLabels names the privileges of es, sorted: a privilege on the
// whole object as itself, one held only on columns as "PRIV (col, ...)".
func privilegeLabels(es []aclEntry) []string {
	whole := map[string]bool{}
	columns := map[string][]string{}
	for _, e := range es {
		if e.column == "" {
			whole[e.privilege] = true
		} else {
			columns[e.privilege] = append(columns[e.privilege], e.column)
		}
	}
	var out []string
	for p := range whole {
		out = append(out, p)
	}
	for p, cols := range columns {
		if !whole[p] {
			out = append(out, p+" ("+strings.Join(sortedUnique(cols), ", ")+")")
		}
	}
	sort.Strings(out)
	return out
}

// revokeAction returns the statement that removes grantee's access to o:
// every privilege, or for a kept role only its grant options. CASCADE also
// removes what the grantee granted on to others.
func revokeAction(o *hubObject, grantee uint32, role, via string) *action {
	keyword := "TABLE"
	if o.kind == FindingKindSequence {
		keyword = "SEQUENCE"
	}
	isFunc := o.kind == FindingKindFunction
	switch {
	case via == FindingViaGrantOption && isFunc:
		return newAction("revoke-grant-option-function", sqlFmtRevokeFunctionGrantOption, rankGrantOption, o.schema, o.name, role)
	case via == FindingViaGrantOption:
		return newAction("revoke-grant-option", sqlFmtRevokeRelationGrantOption, rankGrantOption, keyword, o.schema, o.name, role)
	case grantee == publicOID && isFunc:
		return newAction("revoke-public-function", sqlFmtRevokeFunctionPublic, rankPublicGrant, o.schema, o.name)
	case grantee == publicOID:
		return newAction("revoke-public", sqlFmtRevokeRelationPublic, rankPublicGrant, keyword, o.schema, o.name)
	case isFunc:
		return newAction("revoke-function", sqlFmtRevokeFunction, rankRoleGrant, o.schema, o.name, role)
	default:
		return newAction("revoke", sqlFmtRevokeRelation, rankRoleGrant, keyword, o.schema, o.name, role)
	}
}

// defaultKey groups default-privilege entries.
type defaultKey struct {
	creator uint32
	schema  string
	objType string
	grantee uint32
}

// defaultObjects maps pg_default_acl object types to the plural keyword
// ALTER DEFAULT PRIVILEGES uses and the word findings show.
var defaultObjects = map[string][2]string{
	"r": {"TABLES", "tables"}, "S": {"SEQUENCES", "sequences"}, "f": {"FUNCTIONS", "functions"},
}

// defaultFindings reports default privileges that would give PUBLIC or a
// non-kept role access to objects created later. The owners' entries are
// findings with a fix; another creator's entries are information, since
// they do not apply to the objects the owner creates.
func defaultFindings(c *hubCatalog, kept map[string]bool) (findings, info []finding) {
	privs := map[defaultKey][]string{}
	for _, d := range c.defaults {
		r := c.roles[d.grantee]
		if d.grantee != publicOID && (c.owners[d.grantee] || r.super || d.grantee == c.current || kept[r.name]) {
			continue
		}
		k := defaultKey{d.creator, d.schema, d.objType, d.grantee}
		privs[k] = append(privs[k], d.privilege)
	}
	for k, p := range privs {
		f := defaultFinding(c, k, p)
		if !c.owners[k.creator] {
			f.Note = noteOtherCreator
			info = append(info, f)
			continue
		}
		f.fix = defaultAction(c, k)
		if f.fix == nil {
			f.Note = noteBadName
		}
		findings = append(findings, f)
	}
	return findings, info
}

// defaultFinding describes one grantee of one default-privileges entry.
func defaultFinding(c *hubCatalog, k defaultKey, privs []string) finding {
	where := "in all schemas"
	if k.schema != "" {
		where = "in schema " + k.schema
	}
	object := "default privileges of " + c.roleName(k.creator) + " " + where + " on " + defaultObjects[k.objType][1]
	return finding{PrivilegeFinding: PrivilegeFinding{
		Role: c.roleName(k.grantee), Object: object, Kind: FindingKindDefaultACL,
		Privileges: sortedUnique(privs), Via: FindingViaDefaultACL,
	}}
}

// defaultAction returns the ALTER DEFAULT PRIVILEGES statement that removes
// one grantee from one of the owner's entries.
func defaultAction(c *hubCatalog, k defaultKey) *action {
	creator, keyword, grantee := c.roleName(k.creator), defaultObjects[k.objType][0], c.roleName(k.grantee)
	switch {
	case k.schema != "" && k.grantee == publicOID:
		return newAction("revoke-default-schema-public", sqlFmtRevokeDefaultSchemaPublic, rankDefaultACL, creator, k.schema, keyword)
	case k.schema != "":
		return newAction("revoke-default-schema", sqlFmtRevokeDefaultSchema, rankDefaultACL, creator, k.schema, keyword, grantee)
	case k.grantee == publicOID:
		return newAction("revoke-default-global-public", sqlFmtRevokeDefaultGlobalPublic, rankDefaultACL, creator, keyword)
	default:
		return newAction("revoke-default-global", sqlFmtRevokeDefaultGlobal, rankDefaultACL, creator, keyword, grantee)
	}
}

// edgeFindings reports checked roles that are direct members of a read-all
// role, and checked roles that inherit or can SET ROLE to such a member.
// The caller revokes the direct membership when it holds ADMIN on the role
// and the grantor's privileges; otherwise an administrator's statement is
// given. A role that reaches it through the member shares that fix. Either
// way the change is cluster-wide.
func edgeFindings(c *hubCatalog, checked map[uint32]bool) []finding {
	var out []finding
	seen := map[[2]uint32]bool{}
	for _, e := range c.edges {
		if !checked[e.member] {
			continue
		}
		seen[[2]uint32{e.member, e.role}] = true
		out = append(out, edgeFinding(c, e, e.member))
	}
	for _, e := range c.edges {
		if !checked[e.member] {
			continue
		}
		for x := range checked {
			key := [2]uint32{x, e.role}
			if seen[key] || !c.member[[2]uint32{x, e.member}] {
				continue
			}
			seen[key] = true
			f := edgeFinding(c, e, x)
			f.Note = strings.TrimSuffix("through "+c.roleName(e.member)+"; "+f.Note, "; ")
			out = append(out, f)
		}
	}
	return out
}

// edgeFinding reports role's access through membership e, with e's fix.
func edgeFinding(c *hubCatalog, e roleEdge, role uint32) finding {
	target, member := c.roleName(e.role), c.roleName(e.member)
	f := finding{PrivilegeFinding: PrivilegeFinding{
		Role: c.roleName(role), Object: target, Kind: FindingKindRole, Via: FindingViaMembership, Scope: ScopeClusterWide,
	}}
	if e.revocable {
		f.fix = newAction("revoke-membership", sqlFmtRevokeMembership, rankMembership, target, member, c.roleName(e.grantor))
	}
	if f.fix == nil {
		f.manual = newAction("manual-revoke-membership", sqlFmtManualRevokeMembership, rankMembership, target, member)
	}
	if f.fix == nil && f.manual == nil {
		f.Note = noteBadName
	}
	return f
}

// ownerMemberFindings reports checked roles that inherit a table owner's
// privileges or can SET ROLE to it. Membership in the owner role is never
// changed, only reported: removing it is the administrator's decision.
func ownerMemberFindings(c *hubCatalog, checked map[uint32]bool) []finding {
	var out []finding
	for r := range checked {
		for o := range c.owners {
			if c.member[[2]uint32{r, o}] {
				out = append(out, finding{PrivilegeFinding: PrivilegeFinding{
					Role: c.roleName(r), Object: c.roleName(o), Kind: FindingKindRole, Via: FindingViaOwnerMembership,
				}})
			}
		}
	}
	return out
}

// objectOwnerFindings reports an mtix function or sequence owned by a role
// other than the sync tables' owners, with the statement an administrator
// runs to return it. A superuser owner is out of scope. mtix never changes
// ownership itself.
func objectOwnerFindings(c *hubCatalog) []finding {
	owners := c.ownerNames()
	var out []finding
	for i := range c.objects {
		o := &c.objects[i]
		if o.kind == FindingKindTable || c.owners[o.owner] || c.roles[o.owner].super || len(owners) == 0 {
			continue
		}
		f := finding{PrivilegeFinding: PrivilegeFinding{
			Role: c.roleName(o.owner), Object: o.label(), Kind: o.kind,
			Via: FindingViaObjectOwner, Note: noteObjectOwner,
		}}
		if o.kind == FindingKindFunction {
			f.manual = newAction("manual-owner-function", sqlFmtManualOwnerFunction, rankGuard, o.schema, o.name, owners[0])
		} else {
			f.manual = newAction("manual-owner-sequence", sqlFmtManualOwnerSequence, rankGuard, o.schema, o.name, owners[0])
		}
		out = append(out, f)
	}
	return out
}

// guardFindings reports a TRUNCATE guard that is missing, disabled, or
// calls another function. A missing guard is restored by the guard
// migration and a disabled one is enabled; a guard that calls another
// function is reported, since replacing it is the administrator's call.
func guardFindings(c *hubCatalog) []finding {
	var out []finding
	for _, g := range c.guards {
		f := finding{PrivilegeFinding: PrivilegeFinding{
			Object: g.trigger + " on " + c.schema + "." + g.table, Kind: FindingKindTrigger,
		}}
		switch {
		case g.enabled == "":
			f.Via, f.fix = FindingViaMissing, guardMigrationAction()
		case g.function != guardFunction:
			f.Via, f.Note = FindingViaMissing, "a trigger with this name calls "+g.function+"; drop it, then run mtix sync harden --apply"
		case g.enabled == "O" || g.enabled == "A":
			continue
		default:
			f.Via = FindingViaDisabled
			f.fix = newAction("enable", sqlFmtEnableTrigger, rankGuard, c.schema, g.table, g.trigger)
		}
		out = append(out, f)
	}
	return out
}

// sortFindings orders findings by role, kind, object and via.
func sortFindings(fs []finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Object != b.Object {
			return a.Object < b.Object
		}
		return a.Via < b.Via
	})
}

// sortedUnique returns the distinct values of in, sorted.
func sortedUnique(in []string) []string {
	set := map[string]bool{}
	for _, s := range in {
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
