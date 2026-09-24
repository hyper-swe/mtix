// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"sort"
)

// finding is a PrivilegeFinding with the action that fixes it, or the
// statement an administrator runs when the caller cannot.
type finding struct {
	PrivilegeFinding
	fix    *action
	manual *action
}

// noteBadName explains exposure whose names mtix will not put in a
// statement (SQL Rule 1a).
const noteBadName = "a name is outside the identifier form mtix puts in statements; revoke it by hand"

// computeFindings verifies the catalog against the kept roles (MTIX-95.1)
// and returns the findings, which fail verification, and the information
// items, which do not. Only the owners, superusers, the caller and the
// kept roles may hold access; a kept role may not pass its access on.
func computeFindings(c *hubCatalog, kept map[string]bool) (findings, info []finding) {
	checked := c.checkedRoles(kept)
	findings = append(findings, grantFindings(c, kept)...)
	defaults, other := defaultFindings(c, kept)
	findings = append(findings, defaults...)
	findings = append(findings, edgeFindings(c, checked)...)
	findings = append(findings, ownerMemberFindings(c, checked)...)
	findings = append(findings, residualFindings(c, kept, checked)...)
	findings = append(findings, guardFindings(c)...)
	sortFindings(findings)
	sortFindings(other)
	return findings, other
}

// grantKey groups ACL entries by object and grantee.
type grantKey struct {
	obj     objKey
	grantee uint32
}

// grantFindings reports every privilege on a sync object held by PUBLIC or
// a non-kept role, and every grant option a kept role holds.
func grantFindings(c *hubCatalog, kept map[string]bool) []finding {
	privs := map[grantKey][]string{}
	options := map[grantKey][]string{}
	for _, e := range c.acl {
		if c.object(e.obj) == nil {
			continue
		}
		k := grantKey{e.obj, e.grantee}
		r := c.roles[e.grantee]
		switch {
		case e.grantee == publicOID:
			privs[k] = append(privs[k], e.privilege)
		case c.owners[e.grantee] || r.super || e.grantee == c.current:
		case kept[r.name]:
			if e.grantable {
				options[k] = append(options[k], e.privilege)
			}
		default:
			privs[k] = append(privs[k], e.privilege)
		}
	}
	var out []finding
	for k, p := range privs {
		out = append(out, grantFinding(c, k, p, FindingViaGrant))
	}
	for k, p := range options {
		out = append(out, grantFinding(c, k, p, FindingViaGrantOption))
	}
	return out
}

// grantFinding builds the finding, and its revoke, for one grantee on one
// object.
func grantFinding(c *hubCatalog, k grantKey, privs []string, via string) finding {
	o := c.object(k.obj)
	role := c.roleName(k.grantee)
	f := finding{PrivilegeFinding: PrivilegeFinding{
		Role: role, Object: o.label(), Kind: o.kind, Privileges: sortedUnique(privs), Via: via,
	}}
	f.fix = revokeAction(o, k.grantee, role, via)
	if f.fix == nil {
		f.Note = noteBadName
	}
	return f
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
// role. The caller revokes the membership when it holds ADMIN on the role
// and the grantor's privileges; otherwise an administrator's statement is
// given. Either way the change is cluster-wide.
func edgeFindings(c *hubCatalog, checked map[uint32]bool) []finding {
	var out []finding
	for _, e := range c.edges {
		if !checked[e.member] {
			continue
		}
		role, member := c.roleName(e.role), c.roleName(e.member)
		f := finding{PrivilegeFinding: PrivilegeFinding{
			Role: member, Object: role, Kind: FindingKindRole, Via: FindingViaMembership, Scope: ScopeClusterWide,
		}}
		if e.revocable {
			f.fix = newAction("revoke-membership", sqlFmtRevokeMembership, rankMembership, role, member, c.roleName(e.grantor))
		}
		if f.fix == nil {
			f.manual = newAction("manual-revoke-membership", sqlFmtManualRevokeMembership, rankMembership, role, member)
		}
		if f.fix == nil && f.manual == nil {
			f.Note = noteBadName
		}
		out = append(out, f)
	}
	return out
}

// ownerMemberFindings reports checked roles that have the privileges of a
// table owner. Membership in the owner role is never changed, only
// reported: removing it is the administrator's decision.
func ownerMemberFindings(c *hubCatalog, checked map[uint32]bool) []finding {
	var out []finding
	for r := range checked {
		for o := range c.owners {
			if c.usage[[2]uint32{r, o}] {
				out = append(out, finding{PrivilegeFinding: PrivilegeFinding{
					Role: c.roleName(r), Object: c.roleName(o), Kind: FindingKindRole, Via: FindingViaOwnerMembership,
				}})
			}
		}
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
