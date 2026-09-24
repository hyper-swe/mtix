// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
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
	for _, role := range []string{"PUBLIC", h.anon, h.authenticated, h.unrel, h.reader, h.team} {
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

	for _, role := range []string{"public", h.anon, h.authenticated, h.unrel, h.reader} {
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
	for _, role := range []string{"PUBLIC", h.anon, h.authenticated, h.unrel, h.reader} {
		require.True(t, seen[role], "findings name %s", role)
	}
}

// TestHarden_ReadAllMemberWithoutAdmin_ReportsExposure: when the owner may
// not revoke a pg_read_all_data membership, the dry run and --apply report
// it with the administrator's statement, --apply exits 2, and the
// membership is left as it was (MTIX-95.1).
func TestHarden_ReadAllMemberWithoutAdmin_ReportsExposure(t *testing.T) {
	h := newExposedHub(t, false)
	f := h.f

	out, err := f.harden(h.owner, "--keep-role", h.team)
	require.Equal(t, 2, exitCodeForError(err))
	require.Contains(t, out, "REVOKE pg_read_all_data FROM "+h.reader)

	out, err = f.harden(h.owner, "--apply", "--keep-role", h.team)
	require.Error(t, err)
	require.Equal(t, 2, exitCodeForError(err), "exposure remains: %s", out)
	require.Contains(t, out, "REVOKE pg_read_all_data FROM "+h.reader)
	require.Contains(t, f.privileges(h.reader), "sync_events SELECT", "the membership was not changed")
	require.Empty(t, f.privileges(h.anon), "everything else was revoked")
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
			require.Equal(t, before, h.f.snapshot(), "the failed run rolled back")
		})
	}
}

// TestHarden_CleanHub_NoDDL: `harden --apply` on a hub that already
// verifies clean issues no DDL, which an event trigger that refuses every
// DDL command proves, and exits 0 (MTIX-95.1, F-44).
func TestHarden_CleanHub_NoDDL(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner := f.ownerRole()
	team := f.role("team")
	f.migrateAs(owner)
	f.ddl(`GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA public TO %I`, team)

	out, err := f.harden(owner, "--apply", "--keep-role", team)
	require.NoError(t, err, "the first --apply makes the hub clean: %s", out)

	f.exec(`CREATE FUNCTION mtixt_refuse_ddl() RETURNS event_trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'ddl refused: %', tg_tag; END $$`)
	f.exec(`CREATE EVENT TRIGGER mtixt_refuse_ddl ON ddl_command_start EXECUTE FUNCTION mtixt_refuse_ddl()`)
	err = f.tryAs(owner, `GRANT SELECT ON audit_log TO PUBLIC`)
	require.Error(t, err, "the event trigger refuses the owner's DDL")
	require.Contains(t, err.Error(), "ddl refused: GRANT")

	out, err = f.harden(owner, "--apply", "--keep-role", team)
	require.NoError(t, err, "--apply on a clean hub issues no DDL and exits 0: %s", out)
	require.Contains(t, out, "nothing to change")

	out, err = f.harden(owner, "--keep-role", team)
	require.NoError(t, err, "a dry run on a clean hub exits 0: %s", out)
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
