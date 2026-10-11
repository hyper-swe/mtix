// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.16 (FR-7.8, FR-18.6): when a merge import gives a
// local task the file's uid, every unpushed reference to the old uid moves
// to the new one in the same transaction, so no push or release sends one
// task's changes under two uids. Written red-first against the
// MTIX-95.31.6 code, which left the task's pending events (and a held
// creation) under the uid the task gave up.
// Parallel cases retain owned fixtures and existing behavior assertions.
package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// allOpTypes is every op_type sync_events accepts (FR-18.6).
var allOpTypes = []model.OpType{
	model.OpCreateNode, model.OpUpdateField, model.OpTransitionStatus, model.OpClaim,
	model.OpUnclaim, model.OpDefer, model.OpComment, model.OpLinkDep, model.OpUnlinkDep,
	model.OpDelete, model.OpSetAcceptance, model.OpSetPrompt,
}

// queueEvent inserts a sync_events row naming node and uid (empty: NULL)
// with the given sync status.
func queueEvent(t *testing.T, s *sqlite.Store, id, node, uid string, op model.OpType, status string) {
	t.Helper()
	var u any
	if uid != "" {
		u = uid
	}
	_, err := s.WriteDB().ExecContext(context.Background(), `INSERT INTO sync_events
		(event_id, project_prefix, node_id, op_type, payload, wall_clock_ts, lamport_clock, vector_clock,
		 author_id, author_machine_hash, sync_status, created_at, uid)
		VALUES (?, 'REC', ?, ?, '{}', 1, 1, '{}', 'agent-a', 'm', ?, '2026-01-01T00:00:00Z', ?)`,
		id, node, string(op), status, u)
	require.NoError(t, err)
}

// holdEvent holds id as push does: a source-push quarantine row whose raw
// event carries uid.
func holdEvent(t *testing.T, s *sqlite.Store, id, uid string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"event_id": id, "uid": uid, "node_id": "REC-1"})
	require.NoError(t, err)
	require.NoError(t, s.HoldPushEvents(context.Background(),
		[]sqlite.QuarantinedEvent{{EventID: id, RawEvent: string(raw), Reason: "payload too large"}}, "v"))
}

// eventUID returns the uid of the sync_events row id ("" for NULL).
func eventUID(t *testing.T, s *sqlite.Store, id string) string {
	t.Helper()
	var uid sql.NullString
	require.NoError(t, s.WriteDB().QueryRowContext(context.Background(),
		`SELECT uid FROM sync_events WHERE event_id = ?`, id).Scan(&uid))
	return uid.String
}

// heldUID returns the uid inside the held raw event id.
func heldUID(t *testing.T, s *sqlite.Store, id string) string {
	t.Helper()
	var raw string
	require.NoError(t, s.WriteDB().QueryRowContext(context.Background(),
		`SELECT raw_event FROM sync_quarantine WHERE event_id = ?`, id).Scan(&raw))
	var e struct {
		UID string `json:"uid"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &e))
	return e.UID
}

// TestImportReconcile_MergeAdoptingUID_CarriesUIDOntoUnpushedEvents checks,
// for every event kind and for both merge write paths (the file's content
// equal to the local one, or different), that the adopted uid replaces the
// old one on the task's pending events and its held event, and on nothing
// else: pushed events, another task's events and a pulled quarantine row
// stay as they were.
func TestImportReconcile_MergeAdoptingUID_CarriesUIDOntoUnpushedEvents(t *testing.T) {
	t.Parallel()
	for _, contentChanges := range []bool{false, true} {
		for _, op := range allOpTypes {
			t.Run(fmt.Sprintf("%s, content changes: %v", op, contentChanges), func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				local, teammate := newTestStore(t), newTestStore(t)
				mine := createSameIDTask(t, local, "REC-1", "", 1, backfilledUID(t), "Shared task")
				theirs := createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")
				other := createSameIDTask(t, local, "REC-2", "", 2, taskUID(t), "Other task")
				createSameIDTask(t, teammate, "REC-2", "", 2, other, "Other task")
				if contentChanges {
					_, err := teammate.WriteDB().ExecContext(ctx,
						`UPDATE nodes SET title = 'Retitled', content_hash = 'h-retitled' WHERE id = 'REC-1'`)
					require.NoError(t, err)
				}
				queueEvent(t, local, "pending-1", "REC-1", mine, op, "pending")
				queueEvent(t, local, "pushed-1", "REC-1", mine, op, "pushed")
				queueEvent(t, local, "other-1", "REC-2", other, op, "pending")
				queueEvent(t, local, "nouid-1", "REC-1", "", op, "pending")
				holdEvent(t, local, "pending-1", mine)

				report := mergeFile(t, local, exportOf(t, teammate))
				require.Len(t, report.UIDAdoptions, 1)

				assert.Equal(t, theirs, eventUID(t, local, "pending-1"), "the pending event follows the task")
				assert.Equal(t, theirs, heldUID(t, local, "pending-1"), "the held copy follows the task")
				assert.Equal(t, theirs, eventUID(t, local, "nouid-1"), "a pending event without a uid names the task")
				assert.Equal(t, mine, eventUID(t, local, "pushed-1"), "a pushed event is history")
				assert.Equal(t, other, eventUID(t, local, "other-1"), "another task's event is untouched")
			})
		}
	}
}

// TestImportReconcile_MergeKeepingUID_LeavesEventsAlone checks that a merge
// that adopts nothing rewrites no event.
func TestImportReconcile_MergeKeepingUID_LeavesEventsAlone(t *testing.T) {
	t.Parallel()
	local, teammate := newTestStore(t), newTestStore(t)
	uid := taskUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, uid, "Shared task")
	createSameIDTask(t, teammate, "REC-1", "", 1, uid, "Retitled shared task")
	queueEvent(t, local, "pending-1", "REC-1", uid, model.OpUpdateField, "pending")

	mergeFile(t, local, exportOf(t, teammate))
	assert.Equal(t, uid, eventUID(t, local, "pending-1"))
}

// TestImportReconcile_MergeAdoptingUID_CreationTakesAdoptedUIDAsEventID
// checks that a task's pending creation, whose uid is its own event id
// (ADR-003 §2), takes the adopted uid as its event id too, with its held
// copy and the reason of a held event that names it, so the hub's
// renumber-required outcome, which names the creation's event id, still
// finds the task (RenumberForHubRejection); and that a creation whose new
// id is taken keeps its id.
func TestImportReconcile_MergeAdoptingUID_CreationTakesAdoptedUIDAsEventID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	mine := createSameIDTask(t, local, "REC-1", "", 1, backfilledUID(t), "Shared task")
	theirs := createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")
	queueEvent(t, local, "child-edit", "REC-1", mine, model.OpUpdateField, "pending")
	holdEvent(t, local, mine, mine)
	holdEvent(t, local, "child-edit", mine)
	_, err := local.WriteDB().ExecContext(ctx,
		`UPDATE sync_quarantine SET reason = ? WHERE event_id = 'child-edit'`, "depends on held create of "+mine+" (REC-1)")
	require.NoError(t, err)

	mergeFile(t, local, exportOf(t, teammate))

	var uid, status string
	require.NoError(t, local.WriteDB().QueryRowContext(ctx,
		`SELECT uid, sync_status FROM sync_events WHERE event_id = ? AND op_type = 'create_node'`, theirs).Scan(&uid, &status))
	assert.Equal(t, []string{theirs, "pending"}, []string{uid, status}, "the creation is re-identified")
	assert.Equal(t, theirs, heldUID(t, local, theirs), "its held copy follows")
	var raw, reason string
	require.NoError(t, local.WriteDB().QueryRowContext(ctx,
		`SELECT raw_event FROM sync_quarantine WHERE event_id = ?`, theirs).Scan(&raw))
	assert.Contains(t, raw, `"event_id":"`+theirs+`"`)
	require.NoError(t, local.WriteDB().QueryRowContext(ctx,
		`SELECT reason FROM sync_quarantine WHERE event_id = 'child-edit'`).Scan(&reason))
	assert.Equal(t, "depends on held create of "+theirs+" (REC-1)", reason)
	var old int
	require.NoError(t, local.WriteDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sync_events WHERE event_id = ?`, mine).Scan(&old))
	assert.Zero(t, old, "no event keeps the old id")

	newID, err := local.RenumberForHubRejection(ctx, theirs)
	require.NoError(t, err, "the hub's outcome names the creation by its new id")
	assert.NotEqual(t, "REC-1", newID)
}

// TestImportReconcile_MergeAdoptingUID_AdoptedUIDTakenAsEventID_CreationKeepsItsID
// checks a creation is not re-identified when an event already has the
// adopted uid as its id: its uid column still follows the task.
func TestImportReconcile_MergeAdoptingUID_AdoptedUIDTakenAsEventID_CreationKeepsItsID(t *testing.T) {
	t.Parallel()
	local, teammate := newTestStore(t), newTestStore(t)
	mine := createSameIDTask(t, local, "REC-1", "", 1, backfilledUID(t), "Shared task")
	theirs := createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")
	queueEvent(t, local, theirs, "REC-9", theirs, model.OpCreateNode, "pushed")

	mergeFile(t, local, exportOf(t, teammate))
	assert.Equal(t, theirs, eventUID(t, local, mine))
}

// pullRow quarantines a pulled event (source pull) with the given raw event
// and reason.
func pullRow(t *testing.T, s *sqlite.Store, id, raw, reason string) {
	t.Helper()
	row := quarantineRow(id, raw)
	row.Reason = reason
	quarantineTx(t, s, func(tx *sql.Tx) error { return sqlite.QuarantineEvent(context.Background(), tx, row) })
}

// quarantineOf returns the raw event and reason of the held row id, and
// whether it exists.
func quarantineOf(t *testing.T, s *sqlite.Store, id string) (raw, reason string, ok bool) {
	t.Helper()
	err := s.WriteDB().QueryRowContext(context.Background(),
		`SELECT raw_event, reason FROM sync_quarantine WHERE event_id = ?`, id).Scan(&raw, &reason)
	if err == sql.ErrNoRows {
		return "", "", false
	}
	require.NoError(t, err)
	return raw, reason, true
}

// TestImportReconcile_MergeAdoptingUID_PulledQuarantineRowsUntouched checks
// the carry only touches the task's held PUSH events: a pulled event that
// sits in sync_quarantine keeps its row, raw event and reason, whether its
// event id equals a pending event's id, the creation's old id or the
// adopted uid, and a pulled row that has the adopted uid as its id keeps
// the creation from taking it (the creation keeps its id).
func TestImportReconcile_MergeAdoptingUID_PulledQuarantineRowsUntouched(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		pulledID     func(mine, theirs string) string // event id of the pulled row
		wantCreation func(mine, theirs string) string // the creation's event id afterwards
	}{
		{"a pulled row with a pending event's id", func(_, _ string) string { return "pending-2" },
			func(_, theirs string) string { return theirs }},
		{"a pulled row with the creation's old id", func(mine, _ string) string { return mine },
			func(_, theirs string) string { return theirs }},
		{"a pulled row with the adopted uid as its id", func(_, theirs string) string { return theirs },
			func(mine, _ string) string { return mine }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local, teammate := newTestStore(t), newTestStore(t)
			mine := createSameIDTask(t, local, "REC-1", "", 1, backfilledUID(t), "Shared task")
			theirs := createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")
			queueEvent(t, local, "pending-2", "REC-1", mine, model.OpUpdateField, "pending")
			id := tt.pulledID(mine, theirs)
			raw := fmt.Sprintf(`{"event_id":%q,"uid":%q}`, id, mine)
			reason := "depends on held create of " + mine
			pullRow(t, local, id, raw, reason)

			mergeFile(t, local, exportOf(t, teammate))

			gotRaw, gotReason, ok := quarantineOf(t, local, id)
			require.True(t, ok, "the pulled row stays")
			assert.Equal(t, raw, gotRaw, "its raw event is untouched")
			assert.Equal(t, reason, gotReason, "its reason is untouched")
			assert.Equal(t, theirs, eventUID(t, local, "pending-2"), "the task's pending event follows the task")
			assert.Equal(t, theirs, eventUID(t, local, tt.wantCreation(mine, theirs)), "the creation is where expected")
		})
	}
}

// TestImport_EveryPathThatChangesATasksUID_CarriesItOntoPendingEvents is the
// MTIX-95.31.16 table over the paths that write nodes.uid for a task that
// already had one: the merge when the content is unchanged, the merge when
// it changed, the merge of a task that had no uid (a backfill uid is stamped
// first), and the replace the automatic import runs. Each moves the task's
// pending events to the new uid; a replace that leaves another title at the
// id is a different task, and its events keep their uid. (The paths that
// only give a task its first uid, the backfill on open and on import, change
// no existing uid and leave the events alone.)
func TestImport_EveryPathThatChangesATasksUID_CarriesItOntoPendingEvents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		replace     bool
		retitled    bool // the file's copy has another title (and content)
		localNoUID  bool
		wantCarried bool
	}{
		{"merge, content unchanged", false, false, false, true},
		{"merge, content changed", false, true, false, true},
		{"merge, local task without a uid", false, false, true, true},
		{"replace, same title", true, false, false, true},
		{"replace, another title is another task", true, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			local, teammate := newTestStore(t), newTestStore(t)
			mine := createSameIDTask(t, local, "REC-1", "", 1, backfilledUID(t), "Shared task")
			theirs := createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")
			if tt.retitled {
				_, err := teammate.WriteDB().ExecContext(ctx,
					`UPDATE nodes SET title = 'Retitled', content_hash = 'h-retitled' WHERE id = 'REC-1'`)
				require.NoError(t, err)
			}
			evUID := mine
			if tt.localNoUID {
				_, err := local.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = NULL`)
				require.NoError(t, err)
				evUID = ""
			}
			queueEvent(t, local, "pending-edit", "REC-1", evUID, model.OpUpdateField, "pending")

			if tt.replace {
				_, err := local.Import(ctx, exportOf(t, teammate), sqlite.ImportModeReplace, false)
				require.NoError(t, err)
			} else {
				mergeFile(t, local, exportOf(t, teammate))
			}

			want := evUID
			if tt.wantCarried {
				want = theirs
			}
			assert.Equal(t, want, eventUID(t, local, "pending-edit"))
		})
	}
}
