// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

// escalationRoles are the predefined roles whose members can read or
// write any file or run any program as the server, and so reach every
// table without a privilege on it (MTIX-95.1).
var escalationRoles = map[string]bool{
	"pg_execute_server_program": true,
	"pg_read_server_files":      true,
	"pg_write_server_files":     true,
}

// escalationFindings reports checked roles that can reach every table
// through a role membership (MTIX-95.1): a role that can SET ROLE to a
// superuser (inheriting a superuser's grants gives no superuser power, and
// only a superuser can grant a superuser role), and a member of a
// server-file or server-program role in any way, ADMIN OPTION alone
// included, since that lets it grant the role to itself. mtix never
// changes these memberships; an administrator removes them.
func escalationFindings(c *hubCatalog, checked map[uint32]bool) []finding {
	out := adminPathFindings(c, checked)
	for r := range checked {
		for g, info := range c.roles {
			pair := [2]uint32{r, g}
			switch {
			case info.super && c.canSet[pair]:
				out = append(out, escalationFinding(c, r, g, FindingViaSuperuser,
					"can SET ROLE to a superuser; an administrator removes the membership"))
			case escalationRoles[info.name] && c.member[pair]:
				out = append(out, escalationFinding(c, r, g, FindingViaMembership,
					"can read or write server files or run server programs through this role; "+
						"an administrator removes the membership"))
			}
		}
	}
	return out
}

// adminPathFindings reports checked roles that hold ADMIN OPTION on a role
// that can SET ROLE to a superuser, through any chain of memberships: such
// a role can grant itself that role with SET and act as the superuser
// (MTIX-95.1). A role that can SET ROLE to the superuser itself is
// reported by escalationFindings instead.
func adminPathFindings(c *hubCatalog, checked map[uint32]bool) []finding {
	var out []finding
	seen := map[[2]uint32]bool{}
	for r := range checked {
		for y, supers := range c.superReach {
			if y == r || !c.admin[[2]uint32{r, y}] {
				continue
			}
			for _, s := range supers {
				key := [2]uint32{r, s}
				if seen[key] || c.canSet[key] {
					continue
				}
				seen[key] = true
				out = append(out, escalationFinding(c, r, s, FindingViaSuperuser,
					"can grant itself "+c.roleName(y)+", which can SET ROLE to a superuser; "+
						"an administrator removes the membership"))
			}
		}
	}
	return out
}

// escalationFinding builds one report-only escalation finding.
func escalationFinding(c *hubCatalog, role, target uint32, via, note string) finding {
	f := finding{PrivilegeFinding: PrivilegeFinding{
		Role: c.roleName(role), Object: c.roleName(target), Kind: FindingKindRole, Via: via, Note: note,
	}}
	if via == FindingViaMembership {
		f.Scope = ScopeClusterWide
	}
	return f
}
