// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// exposedHub builds the fixture hub the harden tests share: the owner has
// migrated it, and PUBLIC, both data-API roles, an unrelated role, a
// member of pg_read_all_data and the owner's default privileges all reach
// the sync tables, next to a team role that is meant to keep its access.
type exposedHub struct {
	f                              *hardenFixture
	owner, team, unrel, reader     string
	anon, authenticated, superuser string
}

// newExposedHub creates the exposed fixture hub. withAdmin gives the owner
// ADMIN on pg_read_all_data and makes it the grantor of the reader's
// membership, so the owner is allowed to revoke that membership.
func newExposedHub(t *testing.T, withAdmin bool) *exposedHub {
	t.Helper()
	initTestApp(t)
	f := newHardenFixture(t)
	h := &exposedHub{f: f, owner: f.ownerRole(), team: f.role("team"),
		unrel: f.role("unrel"), reader: f.role("reader"), superuser: f.superuser}
	h.anon, h.authenticated = f.dataAPIRoles()
	f.migrateAs(h.owner)

	// PUBLIC, the data-API roles and an unrelated role reach the tables.
	f.exec(`GRANT SELECT ON ALL TABLES IN SCHEMA public TO PUBLIC`)
	f.ddl(`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I, %I`, h.anon, h.authenticated)
	f.ddl(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %I, %I`, h.anon, h.authenticated)
	f.ddl(`GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO %I`, h.anon)
	f.ddl(`GRANT SELECT ON audit_log TO %I`, h.unrel)
	f.exec(`GRANT SELECT ON audit_log TO pg_monitor`) // a predefined role

	// The team role reads and writes; it also holds a grant option that
	// it used to pass SELECT on to the unrelated role.
	f.ddl(`GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA public TO %I`, h.team)
	f.ddl(`GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO %I`, h.team)
	f.ddl(`GRANT SELECT ON sync_projects TO %I WITH GRANT OPTION`, h.team)
	f.ddlAs(h.team, `GRANT SELECT ON sync_projects TO %I`, h.unrel)

	// A member of the predefined read-all role.
	if withAdmin {
		f.ddl(`GRANT pg_read_all_data TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE`, h.owner)
		f.ddl(`GRANT pg_read_all_data TO %I GRANTED BY %I`, h.reader, h.owner)
	} else {
		f.ddl(`GRANT pg_read_all_data TO %I`, h.reader)
	}

	// The owner's default privileges re-grant every future table, and
	// another creator's global entry does the same for its own tables.
	f.ddl(`ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT ALL ON TABLES TO %I, %I`,
		h.owner, h.anon, h.authenticated)
	f.ddl(`ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT ALL ON SEQUENCES TO %I`,
		h.owner, h.anon)
	f.ddl(`ALTER DEFAULT PRIVILEGES FOR ROLE %I GRANT SELECT ON TABLES TO %I`, h.superuser, h.anon)
	return h
}

// TestHarden_RevokesUnintendedAccess: the dry run lists every role and
// default privilege it would change and changes nothing; `--apply
// --keep-role <team>` then leaves only the owner and the team role with
// access, verification passes, the team still reads and writes, and a
// table the owner creates afterwards is not readable by the data-API roles
// (MTIX-95.1).
func TestHarden_RevokesUnintendedAccess(t *testing.T) {
	h := newExposedHub(t, true)
	f := h.f
	before := f.snapshot()

	out, err := f.harden(h.owner, "--keep-role", h.team)
	require.Error(t, err, "a dry run with changes to make exits 2")
	require.Equal(t, 2, exitCodeForError(err))
	require.Equal(t, before, f.snapshot(), "the dry run changes nothing")
	require.Contains(t, out, "dry run")
	for _, role := range []string{"PUBLIC", h.anon, h.authenticated, h.unrel, h.reader, h.team, "pg_monitor"} {
		require.Contains(t, out, role, "the dry run lists %s", role)
	}
	require.Contains(t, out, "default privileges of "+h.owner+" in schema public on tables")
	require.Contains(t, out, "default privileges of "+h.owner+" in schema public on sequences")
	require.Contains(t, out, "cluster-wide", "a membership revoke is marked cluster-wide")
	require.Contains(t, out, "REVOKE ALL ON TABLE public.audit_log FROM PUBLIC CASCADE")
	require.Contains(t, out, "REVOKE GRANT OPTION FOR ALL ON TABLE public.sync_projects FROM "+h.team+" CASCADE")

	out, err = f.harden(h.owner, "--apply", "--keep-role", h.team)
	require.NoError(t, err, "verification passes after --apply: %s", out)
	require.Contains(t, out, "verification passed")
	require.Contains(t, out, "mtix config set sync.keep_roles "+h.team,
		"the kept role is offered for the config, never written silently")

	for _, role := range []string{"public", h.anon, h.authenticated, h.unrel, h.reader, "pg_monitor"} {
		require.Empty(t, f.privileges(role), "%s keeps no access", role)
	}
	require.Contains(t, f.privileges(h.team), "sync_events SELECT")
	require.NoError(t, f.tryAs(h.team, `SELECT count(*) FROM sync_events`), "the kept role still reads")
	require.NoError(t, f.tryAs(h.team,
		`INSERT INTO audit_log (project_prefix, actor, action) VALUES ('TST', 'team', 'write')`),
		"the kept role still writes")

	f.ddlAs(h.owner, `CREATE TABLE %I (id INT)`, "mtixt_created_later")
	require.Empty(t, f.strings(`
		SELECT r || ' ' || p FROM unnest($1::text[]) AS r
		CROSS JOIN unnest(ARRAY['SELECT','INSERT','UPDATE','DELETE']) AS p
		WHERE pg_catalog.has_table_privilege(r, 'public.mtixt_created_later', p)`,
		[]string{h.anon, h.authenticated}), "a later table is not reachable by the data-API roles")

	out, err = f.harden(h.owner, "--keep-role", h.team)
	require.NoError(t, err, "a dry run on the hardened hub is clean: %s", out)

	// Column privileges are privileges too: to a data-API role and to an
	// unrelated role, on a hub that just verified clean.
	f.ddl(`GRANT SELECT (event_id, payload), UPDATE (payload) ON sync_events TO %I`, h.anon)
	f.ddl(`GRANT SELECT (actor) ON audit_log TO %I`, h.unrel)
	require.Contains(t, f.privileges(h.anon), "sync_events column UPDATE")
	out, err = f.harden(h.owner, "--keep-role", h.team)
	require.Equal(t, 2, exitCodeForError(err), "column privileges fail verification: %s", out)
	require.Contains(t, out, "SELECT (event_id, payload), UPDATE (payload)")
	require.Contains(t, out, "REVOKE ALL ON TABLE public.sync_events FROM anon CASCADE")
	require.Contains(t, out, "REVOKE ALL ON TABLE public.audit_log FROM "+h.unrel+" CASCADE")
	out, err = f.harden(h.owner, "--apply", "--keep-role", h.team)
	require.NoError(t, err, "column privileges are revoked: %s", out)
	require.Empty(t, f.privileges(h.anon))
	require.Empty(t, f.privileges(h.unrel))
}

// TestHarden_JSON_ReportsFindingFields: --json carries the finding fields
// an agent needs: role, object, kind, via and the statement that fixes it.
func TestHarden_JSON_ReportsFindingFields(t *testing.T) {
	h := newExposedHub(t, true)
	app.jsonOutput = true
	out, err := h.f.harden(h.owner, "--keep-role", h.team)
	require.Equal(t, 2, exitCodeForError(err))

	var result struct {
		Applied bool `json:"applied"`
		Before  struct {
			Schema    string   `json:"schema"`
			Owners    []string `json:"owners"`
			KeptRoles []string `json:"kept_roles"`
			Findings  []struct {
				Role, Object, Kind, Via, Fix string
			} `json:"findings"`
			Info []json.RawMessage `json:"info"`
		} `json:"before"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result), "stdout: %s", out)
	require.False(t, result.Applied)
	require.Equal(t, "public", result.Before.Schema)
	require.Equal(t, []string{h.owner}, result.Before.Owners)
	require.Equal(t, []string{h.team}, result.Before.KeptRoles)
	require.NotEmpty(t, result.Before.Info, "another creator's default privileges are information")
	seen := map[string]bool{}
	for _, fd := range result.Before.Findings {
		require.NotEmpty(t, fd.Object)
		require.NotEmpty(t, fd.Kind)
		require.NotEmpty(t, fd.Via)
		require.NotEmpty(t, fd.Fix, "every finding on this hub has a fix")
		seen[fd.Role] = true
	}
	for _, role := range []string{"PUBLIC", h.anon, h.authenticated, h.unrel, h.reader, "pg_monitor"} {
		require.True(t, seen[role], "findings name %s", role)
	}
}

// TestHarden_ReadAllMemberWithoutAdmin_ReportsExposure: when the owner may
// not revoke a pg_read_all_data membership, because it lacks ADMIN on the
// role or because a third role granted the membership, the dry run and
// --apply report it with the administrator's statement, --apply exits 2
// after the other changes, and the membership is left as it was
// (MTIX-95.1).
func TestHarden_ReadAllMemberWithoutAdmin_ReportsExposure(t *testing.T) {
	tests := []struct {
		name      string
		withAdmin bool
		setup     func(h *exposedHub)
	}{
		{"owner has no ADMIN", false, func(*exposedHub) {}},
		{"owner has ADMIN; a third role granted the membership", true, func(h *exposedHub) {
			h.f.ddl(`REVOKE pg_read_all_data FROM %I GRANTED BY %I`, h.reader, h.owner)
			h.f.ddl(`GRANT pg_read_all_data TO %I`, h.reader) // granted by the superuser
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newExposedHub(t, tt.withAdmin)
			tt.setup(h)
			f := h.f

			out, err := f.harden(h.owner, "--keep-role", h.team)
			require.Equal(t, 2, exitCodeForError(err))
			require.Contains(t, out, "REVOKE pg_read_all_data FROM "+h.reader)
			require.NotContains(t, out, "GRANTED BY", "no revoke the owner may not run is planned")

			out, err = f.harden(h.owner, "--apply", "--keep-role", h.team)
			require.Error(t, err)
			require.Equal(t, 2, exitCodeForError(err), "exposure remains: %s", out)
			require.Contains(t, out, "REVOKE pg_read_all_data FROM "+h.reader)
			require.Contains(t, f.privileges(h.reader), "sync_events SELECT", "the membership was not changed")
			require.Empty(t, f.privileges(h.anon), "everything else was revoked")
		})
	}
}

// TestHarden_NonOwner_RefusesWithoutChanges: a caller that does not own
// every sync table gets the fixed refusal and exit 1, and no privilege,
// default privilege, trigger or membership changes, in a dry run and with
// --apply (MTIX-95.1).
func TestHarden_NonOwner_RefusesWithoutChanges(t *testing.T) {
	tests := []struct {
		name  string
		setup func(h *exposedHub) string // returns the acting role
	}{
		{"least-privilege team role", func(h *exposedHub) string { return h.team }},
		{"owner of all but one table", func(h *exposedHub) string {
			h.f.ddl(`ALTER TABLE sync_hub_state OWNER TO %I`, h.unrel)
			return h.owner
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newExposedHub(t, true)
			f := h.f
			f.exec(`ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_truncate`)
			actor := tt.setup(h)
			before := f.snapshot()
			for _, args := range [][]string{{}, {"--apply"}, {"--apply", "--keep-role", h.team}} {
				out, err := f.harden(actor, args...)
				require.Error(t, err, "args %v", args)
				require.Equal(t, 1, exitCodeForError(err))
				require.Contains(t, err.Error(),
					"refused: the connecting role does not own every sync table")
				require.ErrorIs(t, err, transport.ErrHardenNotOwner)
				require.NotContains(t, out, "REVOKE", "a refusal lists no statements")
				require.Equal(t, before, f.snapshot(), "args %v change nothing", args)
			}
		})
	}
}

// TestHarden_RevokeWarningIsFailure: when a REVOKE raises a PostgreSQL
// WARNING because the owner did not grant the privilege (an mtix function
// or sequence owned by another role), --apply fails with exit 1, cites the
// WARNING and names the object, although the driver returned no error; the
// transaction rolls back, so nothing changes (MTIX-95.1).
func TestHarden_RevokeWarningIsFailure(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(h *exposedHub)
		object  string
		warning string
	}{
		{
			name: "function owned by another role",
			setup: func(h *exposedHub) {
				h.f.ddl(`ALTER FUNCTION append_only_no_truncate() OWNER TO %I`, h.unrel)
			},
			object:  "public.append_only_no_truncate()",
			warning: `no privileges could be revoked for "append_only_no_truncate"`,
		},
		{
			name: "sequence owned by another role",
			setup: func(h *exposedHub) {
				// The owner may use the sequence, as inserts need, but did
				// not grant anon's USAGE: its REVOKE can only warn.
				h.f.exec(`ALTER SEQUENCE audit_log_audit_id_seq OWNED BY NONE`)
				h.f.ddl(`ALTER SEQUENCE audit_log_audit_id_seq OWNER TO %I`, h.unrel)
				h.f.ddl(`GRANT USAGE, SELECT ON SEQUENCE audit_log_audit_id_seq TO %I, %I`, h.owner, h.anon)
			},
			object:  "public.audit_log_audit_id_seq",
			warning: `no privileges could be revoked for "audit_log_audit_id_seq"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newExposedHub(t, true)
			tt.setup(h)
			before := h.f.snapshot()
			out, err := h.f.harden(h.owner, "--apply", "--keep-role", h.team)
			require.Error(t, err, out)
			require.Equal(t, 1, exitCodeForError(err), "a WARNING is an error, not remaining exposure")
			require.Contains(t, err.Error(), "WARNING")
			require.Contains(t, err.Error(), tt.warning, "the WARNING itself is cited")
			require.Contains(t, err.Error(), tt.object, "the object is named")
			require.ErrorIs(t, err, transport.ErrHubWarning)
			require.Equal(t, before, h.f.snapshot(), "the failed run rolled back")
		})
	}
}

// TestHarden_CleanHub_NoDDL: `harden --apply` on a hub that already
// verifies clean issues no DDL, which an event trigger that refuses every
// DDL command proves, and exits 0 (MTIX-95.1, F-44). A freshly migrated hub
// is such a hub.
func TestHarden_CleanHub_NoDDL(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner := f.ownerRole()
	team := f.role("team")
	f.migrateAs(owner)
	f.ddl(`GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA public TO %I`, team)

	f.exec(`CREATE FUNCTION mtixt_refuse_ddl() RETURNS event_trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'ddl refused: %', tg_tag; END $$`)
	f.exec(`CREATE EVENT TRIGGER mtixt_refuse_ddl ON ddl_command_start EXECUTE FUNCTION mtixt_refuse_ddl()`)
	err := f.tryAs(owner, `GRANT SELECT ON audit_log TO PUBLIC`)
	require.Error(t, err, "the event trigger refuses the owner's DDL")
	require.Contains(t, err.Error(), "ddl refused: GRANT")

	out, err := f.harden(owner, "--apply", "--keep-role", team)
	require.NoError(t, err, "--apply on a clean hub issues no DDL and exits 0: %s", out)
	require.Contains(t, out, "nothing to change")

	out, err = f.harden(owner, "--keep-role", team)
	require.NoError(t, err, "a dry run on a clean hub exits 0: %s", out)
}

// TestHarden_FreshHub_VerifiesClean: a freshly migrated hub, with no role
// configured and a second superuser present, verifies clean (exit 0). EXECUTE for PUBLIC on the mtix
// trigger functions is reported as information: a trigger function cannot
// be called directly, and triggers fire without EXECUTE (MTIX-95.1).
func TestHarden_FreshHub_VerifiesClean(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner := f.ownerRole()
	f.migrateAs(owner)
	f.ddl(`ALTER ROLE %I SUPERUSER`, f.role("su2")) // superusers are out of scope
	app.jsonOutput = true

	out, err := f.harden(owner)
	require.NoError(t, err, "a fresh hub verifies clean: %s", out)
	var result struct {
		Before struct {
			Findings   []json.RawMessage `json:"findings"`
			Statements []string          `json:"statements"`
			Info       []struct {
				Role, Object, Kind, Fix, Note string
			} `json:"info"`
		} `json:"before"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result), out)
	require.Empty(t, result.Before.Findings)
	require.Empty(t, result.Before.Statements, "--apply would run nothing")
	objects := map[string]bool{}
	for _, i := range result.Before.Info {
		if i.Role == "PUBLIC" && i.Kind == "function" {
			objects[i.Object] = true
			require.Contains(t, i.Note, "trigger function")
		}
	}
	require.Equal(t, map[string]bool{
		"public.append_only_no_truncate()": true, "public.audit_log_immutable()": true,
	}, objects)
}

// TestHarden_RoleMemberships_Reported: access held through a role is
// reported: members of the owner role that inherit it or can SET ROLE to
// it (never changed), a member of pg_read_all_data that can only SET ROLE
// to it, a role that can SET ROLE to such a member, and table and column
// privileges inherited from a role verification does not check
// (MTIX-95.1).
func TestHarden_RoleMemberships_Reported(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner := f.ownerRole()
	inh, setOnly := f.role("inh"), f.role("setonly")
	setReader, grp, viaGrp := f.role("setreader"), f.role("grp"), f.role("viagrp")
	su, viaSu := f.role("su"), f.role("viasu")
	f.migrateAs(owner)

	f.ddl(`GRANT %I TO %I`, owner, inh)
	f.ddl(`GRANT %I TO %I WITH INHERIT FALSE`, owner, setOnly)
	f.ddl(`GRANT pg_read_all_data TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE`, owner)
	f.ddl(`GRANT pg_read_all_data TO %I WITH INHERIT FALSE GRANTED BY %I`, setReader, owner)
	f.ddl(`GRANT pg_read_all_data TO %I GRANTED BY %I`, grp, owner)
	f.ddl(`GRANT %I TO %I WITH INHERIT FALSE`, grp, viaGrp)
	f.ddl(`ALTER ROLE %I SUPERUSER`, su)
	f.ddl(`GRANT REFERENCES, TRIGGER, TRUNCATE ON sync_events TO %I`, su)
	f.ddl(`GRANT UPDATE (payload) ON sync_events TO %I`, su)
	f.ddl(`GRANT %I TO %I WITH INHERIT TRUE, SET FALSE`, su, viaSu)
	app.jsonOutput = true

	type finding struct {
		Role, Object, Kind, Via, Fix string
		Privileges                   []string
	}
	// report runs harden and returns the last verification's findings,
	// keyed by "role object".
	report := func(args ...string) (map[string]finding, error) {
		out, err := f.harden(owner, args...)
		var raw struct {
			Before struct{ Findings []finding }  `json:"before"`
			After  *struct{ Findings []finding } `json:"after"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &raw), out)
		fs := raw.Before.Findings
		if raw.After != nil {
			fs = raw.After.Findings
		}
		byRole := map[string]finding{}
		for _, fd := range fs {
			byRole[fd.Role+" "+fd.Object] = fd
		}
		return byRole, err
	}

	before, err := report()
	require.Equal(t, 2, exitCodeForError(err))
	require.Equal(t, "owner_membership", before[inh+" "+owner].Via, "an inheriting member of the owner")
	require.Equal(t, "owner_membership", before[setOnly+" "+owner].Via, "a SET-only member of the owner")
	require.NotEmpty(t, before[setReader+" pg_read_all_data"].Fix, "a SET-only read-all member is revoked")
	require.NotEmpty(t, before[grp+" pg_read_all_data"].Fix)
	require.NotEmpty(t, before[viaGrp+" pg_read_all_data"].Fix, "a role that can SET ROLE to a read-all member")
	ref := before[viaSu+" public.sync_events"]
	require.Equal(t, "membership", ref.Via)
	require.Equal(t, []string{"REFERENCES", "TRIGGER", "TRUNCATE", "UPDATE"}, ref.Privileges,
		"inherited table and column privileges")

	after, err := report("--apply")
	require.Equal(t, 2, exitCodeForError(err), "owner members and inherited access remain")
	require.Contains(t, after, inh+" "+owner)
	require.Contains(t, after, setOnly+" "+owner)
	require.Contains(t, after, viaSu+" public.sync_events")
	require.NotContains(t, after, setReader+" pg_read_all_data")
	require.NotContains(t, after, grp+" pg_read_all_data")
	require.NotContains(t, after, viaGrp+" pg_read_all_data")
	require.Contains(t, f.strings(`SELECT 1::text WHERE pg_catalog.pg_has_role($1::text, $2::text, 'MEMBER')`, setOnly, owner),
		"1", "owner membership is never changed")
}

// TestHarden_ApplyWhileMigrationLockHeld_FailsFast: --apply takes the
// migration advisory lock with a 5-second lock timeout, so while another
// session holds the lock it fails within the timeout and changes nothing,
// instead of queueing pushes and pulls behind it (MTIX-95.1, F-44). The dry
// run takes no lock and still reports.
func TestHarden_ApplyWhileMigrationLockHeld_FailsFast(t *testing.T) {
	h := newExposedHub(t, true)
	f := h.f
	ctx := context.Background()
	holder, err := f.admin.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(ctx) }()
	_, err = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, transport.AdvisoryLockKey)
	require.NoError(t, err)
	before := f.snapshot()

	_, err = f.harden(h.owner, "--keep-role", h.team)
	require.Equal(t, 2, exitCodeForError(err), "the dry run does not wait for the lock")

	start := time.Now()
	_, err = f.harden(h.owner, "--apply", "--keep-role", h.team)
	elapsed := time.Since(start)
	require.Error(t, err)
	require.Equal(t, 1, exitCodeForError(err))
	require.Contains(t, err.Error(), "lock timeout")
	require.Less(t, elapsed, 9*time.Second, "fails at the 5-second lock timeout, before the statement timeout")
	require.Equal(t, before, f.snapshot(), "nothing changed")
}

// TestHarden_IncompleteSchema_Refuses: harden refuses, with exit 1 and no
// change, when a sync table is missing or the sync tables resolve to more
// than one schema (MTIX-95.1).
func TestHarden_IncompleteSchema_Refuses(t *testing.T) {
	tests := []struct {
		name  string
		setup func(h *exposedHub)
		want  string
	}{
		{"missing table", func(h *exposedHub) { h.f.exec(`DROP TABLE sync_hub_state`) }, "sync_hub_state is missing"},
		{"tables in two schemas", func(h *exposedHub) {
			h.f.ddl(`CREATE SCHEMA mtixt_other AUTHORIZATION %I`, h.owner)
			h.f.exec(`ALTER TABLE sync_hub_state SET SCHEMA mtixt_other`)
			h.f.ddl(`ALTER DATABASE %I SET search_path = public, mtixt_other`, h.f.dbName)
		}, "span schemas"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newExposedHub(t, true)
			tt.setup(h)
			before := h.f.snapshot()
			for _, args := range [][]string{{}, {"--apply"}} {
				_, err := h.f.harden(h.owner, append(args, "--keep-role", h.team)...)
				require.Error(t, err)
				require.Equal(t, 1, exitCodeForError(err))
				require.ErrorIs(t, err, transport.ErrSyncSchemaIncomplete)
				require.Contains(t, err.Error(), tt.want)
				require.Equal(t, before, h.f.snapshot())
			}
		})
	}
}

// TestHarden_UnknownKeptRole_Refuses: a kept role that does not exist is
// refused with exit 1 before anything changes, so a mistyped name never
// leaves the intended role unkept (MTIX-95.1).
func TestHarden_UnknownKeptRole_Refuses(t *testing.T) {
	h := newExposedHub(t, true)
	before := h.f.snapshot()
	for _, args := range [][]string{{"--keep-role", h.team + "_typo"}, {"--apply", "--keep-role", h.team + "_typo"}} {
		_, err := h.f.harden(h.owner, args...)
		require.Error(t, err)
		require.Equal(t, 1, exitCodeForError(err))
		require.Contains(t, err.Error(), "does not exist")
		require.Equal(t, before, h.f.snapshot(), "args %v change nothing", args)
	}
}

// TestHarden_FunctionOwnedByAnotherRole_Reported: an mtix trigger function
// owned by a role other than the sync tables' owner fails verification,
// with the statement an administrator runs to return it, even when no
// privilege needs revoking (MTIX-95.1).
func TestHarden_FunctionOwnedByAnotherRole_Reported(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner, unrel := f.ownerRole(), f.role("unrel")
	f.migrateAs(owner)
	f.ddl(`ALTER FUNCTION append_only_no_truncate() OWNER TO %I`, unrel)

	out, err := f.harden(owner)
	require.Equal(t, 2, exitCodeForError(err), out)
	require.Contains(t, out, "object_owner")
	require.Contains(t, out, "ALTER FUNCTION public.append_only_no_truncate() OWNER TO "+owner)
	out, err = f.harden(owner, "--apply")
	require.Equal(t, 2, exitCodeForError(err), "only an administrator can return it: %s", out)
}

// TestHarden_AdminOnlyMembers_Reported: a role that holds only ADMIN
// OPTION on the owner role or on pg_read_all_data can grant that role to
// itself, so it is reported like any member, and verification does not
// pass (MTIX-95.1).
func TestHarden_AdminOnlyMembers_Reported(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner := f.ownerRole()
	admOwner, admRead := f.role("admowner"), f.role("admread")
	f.migrateAs(owner)
	f.ddl(`GRANT %I TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE`, owner, admOwner)
	f.ddl(`GRANT pg_read_all_data TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE`, admRead)

	out, err := f.harden(owner)
	require.Equal(t, 2, exitCodeForError(err), "ADMIN-only members fail verification: %s", out)
	require.Contains(t, out, admOwner)
	require.Contains(t, out, "owner_membership")
	require.Contains(t, out, admRead)
	require.Contains(t, out, "REVOKE pg_read_all_data FROM "+admRead)
	require.NotContains(t, out, "verification passed")
}

// TestHarden_KeptRoleGrantedByOtherRole_KeepsAccess: a kept role whose
// privileges were granted by a role that is not kept keeps them: --apply
// grants them again from the owner before that role's CASCADE revoke, and
// the dry run says so (MTIX-95.1).
func TestHarden_KeptRoleGrantedByOtherRole_KeepsAccess(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner, lead, team := f.ownerRole(), f.role("lead"), f.role("team")
	f.migrateAs(owner)
	f.ddlAs(owner, `GRANT SELECT, INSERT ON sync_events TO %I WITH GRANT OPTION`, lead)
	f.ddlAs(owner, `GRANT UPDATE (payload) ON sync_events TO %I WITH GRANT OPTION`, lead)
	f.ddlAs(lead, `GRANT SELECT, INSERT ON sync_events TO %I`, team)
	f.ddlAs(lead, `GRANT UPDATE (payload) ON sync_events TO %I`, team)
	onEvents := func(role string) []string { // privileges on sync_events only
		var out []string
		for _, p := range f.privileges(role) {
			if strings.HasPrefix(p, "sync_events ") {
				out = append(out, p)
			}
		}
		return out
	}
	before := onEvents(team)
	require.Equal(t, []string{"sync_events INSERT", "sync_events SELECT", "sync_events column UPDATE"}, before)

	out, err := f.harden(owner, "--keep-role", team)
	require.Equal(t, 2, exitCodeForError(err), out)
	require.Contains(t, out, "Kept roles whose privileges the owner grants again: "+team)
	require.Contains(t, out, "GRANT SELECT ON TABLE public.sync_events TO "+team)
	require.Contains(t, out, "GRANT UPDATE (payload) ON TABLE public.sync_events TO "+team)

	out, err = f.harden(owner, "--apply", "--keep-role", team)
	require.NoError(t, err, "verification passes: %s", out)
	require.Equal(t, before, onEvents(team), "the kept role keeps every privilege")
	require.Empty(t, onEvents(lead), "the role that granted them keeps nothing")
	require.Equal(t, []string{owner}, f.strings(`
		SELECT DISTINCT a.grantor::regrole::text
		FROM pg_catalog.pg_class c CROSS JOIN LATERAL pg_catalog.aclexplode(c.relacl) a
		WHERE c.oid = 'public.sync_events'::regclass AND a.grantee = $1::text::regrole`, team),
		"the owner is now the grantor")
}

// TestHarden_Maintain_PG17: from PostgreSQL 17, MAINTAIN on a sync table and
// membership in pg_maintain are checked like the other privileges and
// read-all roles (MTIX-95.1).
func TestHarden_Maintain_PG17(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	var version int
	require.NoError(t, f.admin.QueryRow(context.Background(),
		`SELECT current_setting('server_version_num')::int`).Scan(&version))
	if version < 170000 {
		t.Skipf("MAINTAIN and pg_maintain exist from PostgreSQL 17; this server is %d", version)
	}
	owner, unrel, maint := f.ownerRole(), f.role("unrel"), f.role("maint")
	su, viaSu := f.role("su"), f.role("viasu")
	f.migrateAs(owner)
	f.ddl(`GRANT MAINTAIN ON sync_events TO %I`, unrel)
	f.ddl(`GRANT pg_maintain TO %I`, maint)
	f.ddl(`ALTER ROLE %I SUPERUSER`, su)
	f.ddl(`GRANT MAINTAIN ON audit_log TO %I`, su)
	f.ddl(`GRANT %I TO %I WITH INHERIT TRUE, SET FALSE`, su, viaSu)

	out, err := f.harden(owner)
	require.Equal(t, 2, exitCodeForError(err), out)
	require.Contains(t, out, "MAINTAIN")
	require.Contains(t, out, unrel)
	require.Contains(t, out, "REVOKE pg_maintain FROM "+maint)
	require.Contains(t, out, viaSu+" ", "MAINTAIN inherited from a role that is not checked")
}
