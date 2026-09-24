// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// installCommandRecorder makes the fixture database record the command tag
// of every DDL command that starts, and the object identity of every
// trigger created, through superuser-owned event triggers.
func installCommandRecorder(f *hardenFixture) {
	f.exec(`CREATE SCHEMA mtixt_audit`)
	f.exec(`CREATE TABLE mtixt_audit.commands (tag TEXT NOT NULL, identity TEXT)`)
	f.exec(`CREATE FUNCTION mtixt_audit.record_start() RETURNS event_trigger
		LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
		BEGIN INSERT INTO mtixt_audit.commands (tag) VALUES (tg_tag); END $$`)
	f.exec(`CREATE EVENT TRIGGER mtixt_record_start ON ddl_command_start
		EXECUTE FUNCTION mtixt_audit.record_start()`)
	f.exec(`CREATE FUNCTION mtixt_audit.record_triggers() RETURNS event_trigger
		LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
		BEGIN
			INSERT INTO mtixt_audit.commands (tag, identity)
			SELECT 'created trigger', object_identity FROM pg_event_trigger_ddl_commands()
			WHERE command_tag = 'CREATE TRIGGER';
		END $$`)
	f.exec(`CREATE EVENT TRIGGER mtixt_record_triggers ON ddl_command_end
		WHEN TAG IN ('CREATE TRIGGER') EXECUTE FUNCTION mtixt_audit.record_triggers()`)
}

// privilegeCommands lists the recorded commands that change privileges.
func privilegeCommands(f *hardenFixture) []string {
	return f.strings(`SELECT tag FROM mtixt_audit.commands
		WHERE tag IN ('GRANT', 'REVOKE', 'ALTER DEFAULT PRIVILEGES') ORDER BY tag`)
}

// guardCreations lists the recorded creations of TRUNCATE guards.
func guardCreations(f *hardenFixture) []string {
	return f.strings(`SELECT identity FROM mtixt_audit.commands
		WHERE tag = 'created trigger' AND identity LIKE '%\_no\_truncate on %' ORDER BY identity`)
}

// runInitAs runs `mtix sync init` connected as role.
func runInitAs(t *testing.T, f *hardenFixture, role string) {
	t.Helper()
	t.Setenv(transport.EnvDSN, f.dsnAs(role))
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncInit(context.Background(), &stdout, &stderr, nil,
		transport.Options{InsecureTLS: true}), stderr.String())
}

// TestSyncInit_ChangesNoPrivileges: `mtix sync init` on a hub where other
// roles hold access changes no privilege and issues no GRANT, REVOKE or
// ALTER DEFAULT PRIVILEGES; it restores a missing TRUNCATE guard, and on a
// hub whose guards are in place it creates none (MTIX-95.1).
func TestSyncInit_ChangesNoPrivileges(t *testing.T) {
	h := newExposedHub(t, true)
	f := h.f
	f.exec(`DROP TRIGGER audit_log_no_truncate ON audit_log`)
	installCommandRecorder(f)
	before := f.snapshot()

	runInitAs(t, f, h.owner)
	require.Empty(t, privilegeCommands(f), "init issues no privilege command")
	require.Equal(t, []string{"audit_log_no_truncate on public.audit_log"}, guardCreations(f),
		"init restores only the missing guard")
	after := f.snapshot()
	require.NotEqual(t, before, after, "the guard is back")
	require.Equal(t, dropTriggerLines(before), dropTriggerLines(after), "no privilege, default privilege or membership changed")

	f.exec(`DELETE FROM mtixt_audit.commands`)
	runInitAs(t, f, h.owner)
	require.Empty(t, privilegeCommands(f))
	require.Empty(t, guardCreations(f), "a second init creates no guard")
}

// dropTriggerLines returns the snapshot lines that are not trigger states.
func dropTriggerLines(snapshot []string) []string {
	var out []string
	for _, l := range snapshot {
		if len(l) < 4 || l[:4] != "trg " {
			out = append(out, l)
		}
	}
	return out
}

// TestSyncPush_IssuesNoDDL: `mtix sync push` completes while an event
// trigger refuses every DDL command, so a push never takes a DDL lock on
// the hub (MTIX-95.1, F-44).
func TestSyncPush_IssuesNoDDL(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner := f.ownerRole()
	f.migrateAs(owner)
	f.exec(`CREATE FUNCTION mtixt_refuse_ddl() RETURNS event_trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'ddl refused: %', tg_tag; END $$`)
	f.exec(`CREATE EVENT TRIGGER mtixt_refuse_ddl ON ddl_command_start EXECUTE FUNCTION mtixt_refuse_ddl()`)
	require.Error(t, f.tryAs(owner, `GRANT SELECT ON audit_log TO PUBLIC`), "the event trigger refuses DDL")

	for i := 0; i < 3; i++ {
		require.NoError(t, runCreate("push-"+string(rune('a'+i)), "", "", 3, "", "", "", "", ""))
	}
	t.Setenv(transport.EnvDSN, f.dsnAs(owner))
	var stdout, stderr bytes.Buffer
	err := runSyncPush(context.Background(), &stdout, &stderr, nil, transport.Options{InsecureTLS: true}, false)
	require.NoError(t, err, "stderr: %s", stderr.String())
	require.Contains(t, stdout.String(), "push complete")
	require.Equal(t, []string{"3"}, f.strings(
		`SELECT count(*)::text FROM sync_events WHERE op_type = 'create_node'`), "the events reached the hub")
}
