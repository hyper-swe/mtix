// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// documentedSmallTeamGrants returns the least-privilege list the
// small-team workflow names (MTIX-95.1.4).
func documentedSmallTeamGrants(t *testing.T) []documentedGrant {
	t.Helper()
	return documentedGrantsIn(t, readRepoFile(t, "internal/docs/templates/workflows/small-team.md.tmpl"))
}

// insertEventAs inserts one create_node row into sync_events as the
// syncing role, with the client-supplied restore_epoch supplied (nil for
// NULL), in a transaction whose session also holds a session-local table
// named sync_hub_state with epoch -7, and returns the event id
// (MTIX-95.1.7).
func (h *leastPrivilegeHub) insertEventAs(t *testing.T, node string, supplied any) string {
	t.Helper()
	h.lamport++
	id := fmt.Sprintf("0193fa00-0000-7000-8000-%012d", 5000+h.lamport)
	tx, err := h.syncPool.Inner().Begin(h.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(h.ctx) }()
	for _, stmt := range []string{
		`CREATE TEMP TABLE sync_hub_state (id BOOLEAN, restore_epoch BIGINT) ON COMMIT DROP`,
		`INSERT INTO pg_temp.sync_hub_state VALUES (TRUE, -7)`,
	} {
		_, err = tx.Exec(h.ctx, stmt)
		require.NoError(t, err, stmt)
	}
	_, err = tx.Exec(h.ctx, `INSERT INTO sync_events (event_id, project_prefix, node_id, uid, op_type,
		payload, wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash, restore_epoch)
		VALUES ($1, 'MTIX', $2, $1, 'create_node', '{"title":"x"}', 1, $3, '{"w":1}', 'w',
		'0123456789abcdef', $4)`, id, node, h.lamport, supplied)
	require.NoError(t, err, "the documented set inserts events")
	require.NoError(t, tx.Commit(h.ctx))
	return id
}

// stampedEpochOf returns the restore_epoch the hub stored for an event.
func (h *leastPrivilegeHub) stampedEpochOf(t *testing.T, id string) string {
	t.Helper()
	got := h.f.strings(`SELECT restore_epoch::text FROM hub_data.sync_events WHERE event_id = $1`, id)
	require.Len(t, got, 1)
	return got[0]
}

// TestLeastPrivilegeRole_ClientSuppliedRestoreEpoch_StoredWithHubEpoch: a
// login role holding exactly the documented least-privilege list inserts
// sync_events rows directly, each with a client-supplied restore epoch
// (negative, larger than the hub's, older than the hub's, NULL), while its
// session holds a session-local table named sync_hub_state; every row is
// stored with the hub's own epoch, before and after the owner's
// mark-restored (MTIX-95.1.7).
func TestLeastPrivilegeRole_ClientSuppliedRestoreEpoch_StoredWithHubEpoch(t *testing.T) {
	grants := documentedSmallTeamGrants(t) // read before initTestApp changes directory
	initTestApp(t)
	h := newLeastPrivilegeHub(t, grants)
	tests := []struct {
		name      string
		restored  bool // the owner has run mark-restored once before the insert
		node      string
		supplied  any
		wantEpoch string
	}{
		{"negative epoch at epoch 0", false, "MTIX-8.1", int64(-1), "0"},
		{"future epoch at epoch 0", false, "MTIX-8.2", int64(5), "0"},
		{"older epoch at epoch 1", true, "MTIX-8.3", int64(0), "1"},
		{"future epoch at epoch 1", true, "MTIX-8.4", int64(42), "1"},
		{"NULL at epoch 1", true, "MTIX-8.5", nil, "1"},
	}
	restored := false
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.restored && !restored {
				_, err := h.ownerPool.MarkRestored(h.ctx)
				require.NoError(t, err)
				restored = true
			}
			id := h.insertEventAs(t, tt.node, tt.supplied)
			require.Equal(t, tt.wantEpoch, h.stampedEpochOf(t, id), "the hub stamps its own epoch")
		})
	}
}

// recordCall is one direct call of the hub's collision recorder.
type recordCall struct {
	prefix, path, eventID, uid string
	wallClock                  int64
}

// callRecorder calls record_restore_collision as the syncing role and
// returns what it reports: whether a collision for the incoming create is
// on record.
func (h *leastPrivilegeHub) callRecorder(t *testing.T, c recordCall) bool {
	t.Helper()
	var onRecord bool
	require.NoError(t, h.syncPool.Inner().QueryRow(h.ctx,
		`SELECT record_restore_collision($1, $2, $3, $4, $5)`,
		c.prefix, c.path, c.eventID, c.uid, c.wallClock).Scan(&onRecord),
		"the documented set executes the recorder")
	return onRecord
}

// collisionRows lists every collision row as the superuser sees it.
func (h *leastPrivilegeHub) collisionRows(t *testing.T) []string {
	t.Helper()
	return h.f.strings(`SELECT concat_ws('|', project_prefix, display_path, held_event_id, held_uid,
		held_epoch, incoming_event_id, incoming_uid, incoming_wall_clock_ts, detected_epoch, status)
		FROM hub_data.sync_node_collisions ORDER BY collision_id`)
}

// TestLeastPrivilegeRole_CollisionRecords_OnlyGenuineCrossEpochThroughHub:
// the documented least-privilege role cannot INSERT into
// sync_node_collisions. It executes record_restore_collision, which
// records nothing unless a different create, stamped in an epoch earlier
// than the hub's current one, holds the number: not in the same epoch, not
// for a free number, another project, the held node's own uid or event id,
// a number only a non-create event names, or an incoming event id already
// on the hub. A genuine cross-epoch call is
// recorded once, with the held create, its epoch and the detected epoch
// read from the hub, and the incoming create's own uid; an empty uid is
// recorded as the event id (MTIX-95.1.7).
func TestLeastPrivilegeRole_CollisionRecords_OnlyGenuineCrossEpochThroughHub(t *testing.T) {
	grants := documentedSmallTeamGrants(t) // read before initTestApp changes directory
	initTestApp(t)
	h := newLeastPrivilegeHub(t, grants)
	held := h.event("MTIX-2.1", "alice", model.OpCreateNode, `{"title":"held"}`)
	onlyUpdate := h.event("MTIX-3.1", "alice", model.OpUpdateField, `{"field_name":"title","new_value":"\"x\""}`)
	h.push(t, held, onlyUpdate)
	incoming := recordCall{"MTIX", "MTIX-2.1", "0193fa00-0000-7000-8000-00000000f001",
		"0193fa00-0000-7000-8000-00000000f001", 777}

	require.False(t, h.callRecorder(t, incoming), "the same epoch records nothing")
	require.Empty(t, h.collisionRows(t))
	_, err := h.ownerPool.MarkRestored(h.ctx)
	require.NoError(t, err)

	_, err = h.syncPool.Inner().Exec(h.ctx, `INSERT INTO sync_node_collisions
		(project_prefix, display_path, held_event_id, held_uid, held_epoch, held_wall_clock_ts,
		 incoming_event_id, incoming_uid, incoming_wall_clock_ts, detected_epoch)
		VALUES ('MTIX', 'MTIX-2.1', $1, $1, -1, 1, 'x', 'x', 1, 9)`, held.EventID)
	requireDenied(t, err, "sync_node_collisions", "the documented set holds no INSERT on sync_node_collisions")

	for _, c := range []struct {
		name string
		call recordCall
	}{
		{"a number no create holds", recordCall{"MTIX", "MTIX-9.9", incoming.eventID, incoming.uid, 1}},
		{"another project", recordCall{"OTHER", "MTIX-2.1", incoming.eventID, incoming.uid, 1}},
		{"the held node's own uid", recordCall{"MTIX", "MTIX-2.1", incoming.eventID, held.UID, 1}},
		{"the held create's own event id", recordCall{"MTIX", "MTIX-2.1", held.EventID, incoming.uid, 1}},
		{"a number only an update names", recordCall{"MTIX", "MTIX-3.1", incoming.eventID, incoming.uid, 1}},
		{"an incoming event id already on the hub", recordCall{"MTIX", "MTIX-2.1", onlyUpdate.EventID, incoming.uid, 1}},
	} {
		require.Falsef(t, h.callRecorder(t, c.call), "%s records nothing", c.name)
		require.Emptyf(t, h.collisionRows(t), "%s leaves no row", c.name)
	}

	require.True(t, h.callRecorder(t, incoming), "a genuine cross-epoch create is recorded")
	require.True(t, h.callRecorder(t, incoming), "a repeated call reports it on record")
	blankUID := recordCall{"MTIX", "MTIX-2.1", "0193fa00-0000-7000-8000-00000000f002", "", 778}
	require.True(t, h.callRecorder(t, blankUID))
	ownUID := recordCall{"MTIX", "MTIX-2.1", "0193fa00-0000-7000-8000-00000000f003",
		"0193fa00-0000-7000-8000-00000000f0aa", 779}
	require.True(t, h.callRecorder(t, ownUID), "a create whose uid differs from its event id")
	require.Equal(t, []string{
		"MTIX|MTIX-2.1|" + held.EventID + "|" + held.UID + "|0|" + incoming.eventID + "|" + incoming.uid + "|777|1|open",
		"MTIX|MTIX-2.1|" + held.EventID + "|" + held.UID + "|0|" + blankUID.eventID + "|" + blankUID.eventID + "|778|1|open",
		"MTIX|MTIX-2.1|" + held.EventID + "|" + held.UID + "|0|" + ownUID.eventID + "|" + ownUID.uid + "|779|1|open",
	}, h.collisionRows(t), "one row per incoming create, with its uid, and the hub's held create and epochs")
}

// TestLeastPrivilegeRole_AfterMarkRestored_CrossEpochCreateRecordedByHub:
// with the documented least-privilege list, a create pushed after the
// owner's mark-restored for a number an earlier-epoch create holds is
// still detected as a restore collision and recorded on the hub, held in
// epoch 0 and detected in epoch 1, and is not inserted; a same-epoch race
// after the restore still renumbers (MTIX-95.1.7).
func TestLeastPrivilegeRole_AfterMarkRestored_CrossEpochCreateRecordedByHub(t *testing.T) {
	grants := documentedSmallTeamGrants(t) // read before initTestApp changes directory
	initTestApp(t)
	h := newLeastPrivilegeHub(t, grants)
	held := h.event("MTIX-4.1", "alice", model.OpCreateNode, `{"title":"held"}`)
	h.push(t, held)
	_, err := h.ownerPool.MarkRestored(h.ctx)
	require.NoError(t, err)

	incoming := h.event("MTIX-4.1", "bob", model.OpCreateNode, `{"title":"incoming"}`)
	_, renumbers, collisions := h.push(t, incoming)
	require.Empty(t, renumbers)
	require.Len(t, collisions, 1, "the cross-epoch create is a restore collision")
	require.Equal(t, held.EventID, collisions[0].HeldEventID)
	open, err := h.syncPool.ListOpenCollisions(h.ctx, "MTIX")
	require.NoError(t, err)
	require.Len(t, open, 1)
	require.Equal(t, incoming.EventID, open[0].IncomingEventID)
	require.Equal(t, [2]int64{0, 1}, [2]int64{open[0].HeldEpoch, open[0].DetectedEpoch})
	require.Empty(t, h.f.strings(`SELECT event_id FROM hub_data.sync_events WHERE event_id = $1`, incoming.EventID),
		"the blocked create is not inserted")

	h.push(t, h.event("MTIX-4.2", "alice", model.OpCreateNode, `{"title":"a"}`))
	_, renumbers, collisions = h.push(t, h.event("MTIX-4.2", "bob", model.OpCreateNode, `{"title":"b"}`))
	require.Empty(t, collisions, "a same-epoch race after the restore is not a restore collision")
	require.Len(t, renumbers, 1, "it renumbers")
}

// schemaCurrentCheck returns the schema current check of a doctor run
// with the DSN dsn.
func schemaCurrentCheck(t *testing.T, dsn string) (pass, warn bool, detail, fix string, err error) {
	t.Helper()
	t.Setenv(transport.EnvDSN, dsn)
	report, err := runDoctorReport(t)
	pass, warn, detail, fix = doctorCheckNamed(t, report, "schema current")
	return pass, warn, detail, fix, err
}

// TestDoctorSchemaCurrent_HubWithoutMigration017_WarnsWithInitFix: on a hub
// without migration 017's stamp trigger, stamp function and collision
// recorder, the doctor's schema current check names each and gives the
// fix, mtix sync init as the table owner; it is a WARN by default and a
// FAIL in strict mode, and either way the later hub checks still run.
// After the fix it passes (MTIX-95.1.7).
func TestDoctorSchemaCurrent_HubWithoutMigration017_WarnsWithInitFix(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	pool := hubPool(t, dsn)
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS sync_events_stamp_restore_epoch ON sync_events`,
		`DROP FUNCTION IF EXISTS hub_stamp_restore_epoch()`,
		`DROP FUNCTION IF EXISTS record_restore_collision(text, text, text, text, bigint)`,
	} {
		_, err := pool.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	pass, warn, detail, fix, err := schemaCurrentCheck(t, dsn)
	require.NoError(t, err, "a WARN keeps the doctor's exit code 0")
	require.True(t, pass, detail)
	require.True(t, warn, "a hub without migration 017 warns: %s", detail)
	for _, want := range []string{"migration 017", "function record_restore_collision(text, text, text, text, bigint)",
		"function hub_stamp_restore_epoch()", "trigger sync_events_stamp_restore_epoch on public.sync_events"} {
		require.Contains(t, detail, want)
	}
	require.Equal(t, "as the table owner (postgres): mtix sync init", fix)
	report, _ := runDoctorReport(t)
	_, _, privDetail, _ := doctorCheckNamed(t, report, "hub-privileges")
	require.NotContains(t, privDetail, "skipped", "the later hub checks still run")

	setKeepRoles(t, "mtix_team")
	pass, _, _, _, err = schemaCurrentCheck(t, dsn)
	require.ErrorIs(t, err, errDoctorChecksFailed)
	require.False(t, pass, "strict mode fails the check")
	report, _ = runDoctorReport(t)
	_, _, triggersDetail, _ := doctorCheckNamed(t, report, "hub-triggers")
	require.NotContains(t, triggersDetail, "skipped", "in strict mode too, the later hub checks still run")
	setKeepRoles(t, "")

	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncInit(ctx, &stdout, &stderr, nil, cloudOpts), stderr.String())
	pass, warn, detail, fix, err = schemaCurrentCheck(t, dsn)
	require.NoError(t, err)
	require.True(t, pass, detail)
	require.False(t, warn, "mtix sync init adds migration 017: %s", detail)
	require.Empty(t, fix)
}

// TestDoctorSchemaCurrent_KeepRolesConfig_GradesGapByMode: on a hub
// without migration 017's stamp trigger, the schema current check is a
// WARN when sync.keep_roles is unset, and a FAIL, not a WARN, when it is
// set or cannot be read (a hand-edited, invalid value): strict mode was
// meant. The hub-privileges check names the unreadable key (MTIX-95.1.7).
func TestDoctorSchemaCurrent_KeepRolesConfig_GradesGapByMode(t *testing.T) {
	tests := []struct {
		name       string
		keep       func(t *testing.T)
		wantPass   bool
		wantWarn   bool
		privDetail string // what the hub-privileges check names, if anything
	}{
		{"unset", func(*testing.T) {}, true, true, ""},
		{"set", func(t *testing.T) { setKeepRoles(t, "mtix_team") }, false, false, ""},
		{"unreadable", func(t *testing.T) { require.NoError(t, writeRawKeepRoles(t, "a,,b")) }, false, false,
			"sync.keep_roles in .mtix/config.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsn := requireCmdPG(t)
			_ = openCmdHub(t)
			initTestApp(t)
			_, err := hubPool(t, dsn).Exec(context.Background(),
				`DROP TRIGGER sync_events_stamp_restore_epoch ON sync_events`)
			require.NoError(t, err)
			tt.keep(t)

			report, err := runDoctorReport(t)
			if tt.wantPass {
				require.NoError(t, err, "a WARN keeps the doctor's exit code 0")
			} else {
				require.ErrorIs(t, err, errDoctorChecksFailed)
			}
			pass, warn, detail, fix := doctorCheckNamed(t, report, schemaCurrentName)
			require.Equal(t, tt.wantPass, pass, detail)
			require.Equal(t, tt.wantWarn, warn, detail)
			require.Equal(t, !tt.wantPass, strings.Contains(detail, "strict mode"), detail)
			require.Contains(t, detail, "trigger sync_events_stamp_restore_epoch on public.sync_events")
			require.Equal(t, "as the table owner (postgres): mtix sync init", fix)
			_, _, privDetail, _ := doctorCheckNamed(t, report, hubPrivilegesName)
			require.Contains(t, privDetail, tt.privDetail)
		})
	}
}

// TestDoctorSchemaCurrent_RoleWithoutExecute_WarnsWithGrantFix: a syncing
// role that holds the documented list except EXECUTE on
// record_restore_collision gets a schema current WARN naming the exact
// GRANT the table owner runs; once it runs, the check passes
// (MTIX-95.1.7).
func TestDoctorSchemaCurrent_RoleWithoutExecute_WarnsWithGrantFix(t *testing.T) {
	initTestApp(t)
	var grants []documentedGrant
	for _, g := range expectedSyncGrants() {
		if g.privilege != "EXECUTE" {
			grants = append(grants, g)
		}
	}
	h := newLeastPrivilegeHub(t, grants)

	pass, warn, detail, fix, err := schemaCurrentCheck(t, h.syncDSN)
	require.NoError(t, err)
	require.True(t, pass, detail)
	require.True(t, warn, "a role that cannot execute the recorder warns: %s", detail)
	require.Contains(t, detail, "cannot execute record_restore_collision")
	grant := "GRANT EXECUTE ON FUNCTION hub_data.record_restore_collision(text, text, text, text, bigint) TO " +
		h.syncer + ";"
	require.Equal(t, "as the table owner ("+h.owner+"): "+grant, fix)

	require.NoError(t, h.f.tryAs(h.owner, grant), "the printed fix runs as printed")
	pass, warn, detail, _, err = schemaCurrentCheck(t, h.syncDSN)
	require.NoError(t, err)
	require.True(t, pass, detail)
	require.False(t, warn, detail)
}

// TestDoctorSchemaCurrent_StampTriggerOnAnotherFunction_ListedWithInitFix:
// a trigger named sync_events_stamp_restore_epoch that executes another
// function than hub_stamp_restore_epoch counts as missing in the schema
// current check, with mtix sync init as the fix; after init the trigger
// executes the stamp function and the check passes (MTIX-95.1.7).
func TestDoctorSchemaCurrent_StampTriggerOnAnotherFunction_ListedWithInitFix(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	pool := hubPool(t, dsn)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS mtix_test_other_stamp() CASCADE`)
		require.NoError(t, err)
	})
	for _, stmt := range []string{
		`CREATE FUNCTION mtix_test_other_stamp() RETURNS trigger AS $$ BEGIN RETURN NEW; END $$ LANGUAGE plpgsql`,
		`DROP TRIGGER sync_events_stamp_restore_epoch ON sync_events`,
		`CREATE TRIGGER sync_events_stamp_restore_epoch BEFORE INSERT ON sync_events
		 FOR EACH ROW EXECUTE FUNCTION mtix_test_other_stamp()`,
	} {
		_, err := pool.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	pass, warn, detail, fix, err := schemaCurrentCheck(t, dsn)
	require.NoError(t, err)
	require.True(t, pass, detail)
	require.True(t, warn, detail)
	require.Contains(t, detail, "trigger sync_events_stamp_restore_epoch on public.sync_events")
	require.NotContains(t, detail, "function hub_stamp_restore_epoch()", "the stamp function itself is present")
	require.Equal(t, "as the table owner (postgres): mtix sync init", fix)

	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncInit(ctx, &stdout, &stderr, nil, cloudOpts), stderr.String())
	pass, warn, detail, _, err = schemaCurrentCheck(t, dsn)
	require.NoError(t, err)
	require.True(t, pass, detail)
	require.False(t, warn, "init binds the trigger to the stamp function: %s", detail)
}

// TestDoctorSchemaCurrent_RoleWithCollisionInsert_WarnsWithRevokeFix: a
// syncing role that holds the documented list plus INSERT on
// sync_node_collisions and USAGE on its sequence gets a schema current
// WARN naming both, with the exact REVOKE statements the table owner runs;
// strict mode fails it; once the statements run, the check passes. A DSN
// naming the table owner is never reported for them (MTIX-95.1.7).
func TestDoctorSchemaCurrent_RoleWithCollisionInsert_WarnsWithRevokeFix(t *testing.T) {
	initTestApp(t)
	grants := append(expectedSyncGrants(),
		documentedGrant{"INSERT", "TABLE", "sync_node_collisions", ""},
		documentedGrant{"USAGE", "SEQUENCE", "sync_node_collisions_collision_id_seq", ""})
	h := newLeastPrivilegeHub(t, grants)

	pass, warn, detail, fix, err := schemaCurrentCheck(t, h.syncDSN)
	require.NoError(t, err, "a WARN keeps the doctor's exit code 0")
	require.True(t, pass, detail)
	require.True(t, warn, "a role holding INSERT on sync_node_collisions warns: %s", detail)
	require.Contains(t, detail, "INSERT on sync_node_collisions")
	require.Contains(t, detail, "USAGE on sync_node_collisions_collision_id_seq")
	revokes := "REVOKE INSERT ON TABLE hub_data.sync_node_collisions FROM " + h.syncer + "; " +
		"REVOKE USAGE ON SEQUENCE hub_data.sync_node_collisions_collision_id_seq FROM " + h.syncer + ";"
	require.Equal(t, "as the table owner ("+h.owner+"): "+revokes, fix)

	setKeepRoles(t, h.syncer)
	pass, _, _, _, err = schemaCurrentCheck(t, h.syncDSN)
	require.ErrorIs(t, err, errDoctorChecksFailed)
	require.False(t, pass, "strict mode fails the check")
	setKeepRoles(t, "")

	pass, warn, detail, _, err = schemaCurrentCheck(t, withSearchPath(t, h.f.dsnAs(h.owner)))
	require.NoError(t, err)
	require.True(t, pass, detail)
	require.False(t, warn, "the table owner is not reported: %s", detail)

	require.NoError(t, h.f.tryAs(h.owner, revokes), "the printed fix runs as printed")
	pass, warn, detail, _, err = schemaCurrentCheck(t, h.syncDSN)
	require.NoError(t, err)
	require.True(t, pass, detail)
	require.False(t, warn, detail)
}
