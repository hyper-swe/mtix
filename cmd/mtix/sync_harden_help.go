// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// syncHardenLong is the help of mtix sync harden: what it checks and
// changes, who may run it, what the check covers (superusers and the
// REPLICATION role attribute are outside it) and its exit codes
// (MTIX-95.1, MTIX-95.1.4).
const syncHardenLong = `Check which roles can use the hub's sync tables, sequences and mtix
functions, and with --apply restrict them to the owner and the roles you
keep. Without --apply this is a dry run: it lists every role, default
privilege and membership it would change, and changes nothing.

With --apply, in one transaction, it revokes every privilege on those
objects, column privileges included, from PUBLIC, from the roles a data
API uses for anonymous and signed-in callers, and from every other role
except the table owner, superusers and the roles named with --keep-role
or in the sync.keep_roles config key. A kept role keeps its privileges,
and its members keep them through it, but it loses any right to grant
them on; the owner first grants again anything a kept role holds by
another role's grant. The owner's default privileges that would give
those roles access to tables created later are revoked too. A membership
in pg_read_all_data, pg_write_all_data or pg_maintain, even one with only
ADMIN OPTION, is revoked when the owner may do so; it is cluster-wide. A
missing TRUNCATE guard is restored, one whose trigger executes another
function (compared by OID) is replaced, and a disabled one, or one that
fires only in replication sessions, is enabled. A server WARNING fails the run
and nothing changes. An --apply that would restore or replace a guard
refuses, changing nothing, when the first schema on the search_path is not
the schema of the sync tables; privilege changes and the dry run are not
affected. Access it cannot remove is reported with the
statement an administrator runs, and after --apply every finding that
remains is listed. EXECUTE on the mtix trigger functions and other roles'
default privileges are information and never fail verification.

Run it as the role that owns the sync tables, or as a superuser or a
member of the owner role; any other role is refused and nothing changes.
Superusers are not checked. The REPLICATION role attribute is outside the
check too: a role that has it is checked for its privileges and
memberships like any other role, but not for the attribute, so review the
roles that have it (rolreplication in pg_roles). The connecting role is
checked unless it owns the sync tables, is a superuser or is kept, so a
member of the owner role reports its own membership: run as the owner, or
keep the role, to verify clean. Review the dry run's role list before
--apply: a role you do not keep loses its access.

Exit code: 0 when verification passes, 2 when changes are pending (dry
run) or access remains (--apply), 1 on an error or a refusal. --json
prints the report for agents and CI.`
