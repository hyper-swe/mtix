// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/stretchr/testify/require"
)

// Own-event replay regressions for MTIX-95.2 (ADR-006 D1-D3; scenarios S1,
// S2, S3 and S7 of the phase-0 reproduction).
//
// `mtix sync pull` fetches every hub event past the cursor, including the
// events this client pushed itself. Each test emits through real store
// mutations, reads the pending projection exactly as the 0.5.x push ships
// it (the hub stores it verbatim and returns it on pull), marks it pushed,
// and feeds the same events back through IdempotentApply in one
// transaction, the way cmd/mtix/sync_pull.go applies a batch.

// scanWireEvents decodes rows selected with the push projection columns
// (event_id … author_machine_hash, no uid: the 0.5.x push does not send it,
// so the hub stores NULL and pull returns it empty).
func scanWireEvents(t *testing.T, rows *sql.Rows) []*model.SyncEvent {
	t.Helper()
	defer func() { _ = rows.Close() }()
	var out []*model.SyncEvent
	for rows.Next() {
		var (
			e                    model.SyncEvent
			opType, payload, vec string
		)
		require.NoError(t, rows.Scan(&e.EventID, &e.ProjectPrefix, &e.NodeID, &opType, &payload,
			&e.WallClockTS, &e.LamportClock, &vec, &e.AuthorID, &e.AuthorMachineHash))
		e.OpType = model.OpType(opType)
		e.Payload = json.RawMessage(payload)
		require.NoError(t, json.Unmarshal([]byte(vec), &e.VectorClock))
		out = append(out, &e)
	}
	require.NoError(t, rows.Err())
	return out
}

// pushPendingOwnEvents plays a successful push: it reads every pending event
// with the projection cmd/mtix/sync_push.go readPendingBatch ships, in the
// same lamport order, and marks each one pushed, as markPushed does.
func pushPendingOwnEvents(t *testing.T, raw *sql.DB) []*model.SyncEvent {
	t.Helper()
	// Pending projection, identical to the 0.5.x push (lamport order).
	rows, err := raw.Query(`
		SELECT event_id, project_prefix, node_id, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash
		  FROM sync_events
		 WHERE sync_status = 'pending'
		 ORDER BY lamport_clock ASC`)
	require.NoError(t, err)
	evs := scanWireEvents(t, rows)
	require.NotEmpty(t, evs, "the mutations under test must have emitted events")
	for _, e := range evs {
		// Mark the event accepted by the hub, as markPushed does.
		_, err := raw.Exec(`UPDATE sync_events SET sync_status = 'pushed' WHERE event_id = ?`, e.EventID)
		require.NoError(t, err)
	}
	return evs
}

// readWireEvent returns one event, by id, with the push projection columns.
func readWireEvent(t *testing.T, raw *sql.DB, eventID string) *model.SyncEvent {
	t.Helper()
	// Single event by primary key, push projection columns.
	rows, err := raw.Query(`
		SELECT event_id, project_prefix, node_id, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash
		  FROM sync_events
		 WHERE event_id = ?`, eventID)
	require.NoError(t, err)
	evs := scanWireEvents(t, rows)
	require.Len(t, evs, 1)
	return evs[0]
}

// latestUpdateFieldEventID returns the newest update_field event id.
func latestUpdateFieldEventID(t *testing.T, raw *sql.DB) string {
	t.Helper()
	var id string
	require.NoError(t, raw.QueryRow(`
		SELECT event_id FROM sync_events
		 WHERE op_type = 'update_field'
		 ORDER BY lamport_clock DESC LIMIT 1`).Scan(&id))
	return id
}

// pullEvents plays a pull batch: every event goes through IdempotentApply
// inside one transaction, in the order given (the hub's lamport order).
func pullEvents(t *testing.T, s *sqlite.Store, evs []*model.SyncEvent) {
	t.Helper()
	require.NoError(t, applyEventsInTx(s, evs))
}

// applyEventsInTx applies evs in one transaction and returns the error.
func applyEventsInTx(s *sqlite.Store, evs []*model.SyncEvent) error {
	ctx := context.Background()
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range evs {
			if err := sqlite.IdempotentApply(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

func setDescription(t *testing.T, s *sqlite.Store, id, v string) {
	t.Helper()
	require.NoError(t, s.UpdateNode(context.Background(), id, &store.NodeUpdate{Description: &v}))
}

func nodeDescription(t *testing.T, raw *sql.DB, id string) string {
	t.Helper()
	var d sql.NullString
	require.NoError(t, raw.QueryRow(`SELECT description FROM nodes WHERE id = ?`, id).Scan(&d))
	return d.String
}

func nodeStatusAndClosedAt(t *testing.T, raw *sql.DB, id string) (string, sql.NullString) {
	t.Helper()
	var (
		status   string
		closedAt sql.NullString
	)
	require.NoError(t, raw.QueryRow(`SELECT status, closed_at FROM nodes WHERE id = ?`, id).
		Scan(&status, &closedAt))
	return status, closedAt
}

func countRows(t *testing.T, raw *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRow(query, args...).Scan(&n))
	return n
}

func countConflictRows(t *testing.T, raw *sql.DB) int {
	t.Helper()
	return countRows(t, raw, `SELECT COUNT(*) FROM sync_conflicts`)
}

// S1 — the reported flow: push, then a newer local edit, then pull.
func TestIdempotentApply_OwnEventAfterNewerLocalEdit_NoConflict(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	setDescription(t, s, "MTIX-1", "v1")
	pushed := pushPendingOwnEvents(t, raw)
	setDescription(t, s, "MTIX-1", "v2") // newer local edit, still pending

	pullEvents(t, s, pushed) // the cursor never moved: the hub returns our own push

	require.Zero(t, countConflictRows(t, raw),
		"replaying this client's own pushed event must not log a conflict")
	require.Equal(t, "v2", nodeDescription(t, raw, "MTIX-1"),
		"the newer local value must survive the replay")
	require.Equal(t, 1, countRows(t, raw,
		`SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`),
		"the newer local edit stays queued for the next push")
}

// S2 — pull straight after push; the field was written twice this cycle.
func TestIdempotentApply_OwnEventsReplayed_NoDuplicateConflicts(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	setDescription(t, s, "MTIX-1", "v1")
	setDescription(t, s, "MTIX-1", "v2")
	pushed := pushPendingOwnEvents(t, raw)

	pullEvents(t, s, pushed)
	pullEvents(t, s, pushed) // the same page again, e.g. before the cursor write

	require.Zero(t, countConflictRows(t, raw),
		"replaying own events must log no conflict, duplicated or otherwise")
	require.Equal(t, "v2", nodeDescription(t, raw, "MTIX-1"))
	require.Equal(t, len(pushed), countRows(t, raw, `SELECT COUNT(*) FROM applied_events`),
		"each own event is acknowledged exactly once")
}

// S3 — the field is written once this cycle, but an earlier same-field
// event from a previous push/pull cycle is already in the log.
func TestIdempotentApply_OwnEventWithFieldHistory_NoConflict(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	setDescription(t, s, "MTIX-1", "v1")
	pullEvents(t, s, pushPendingOwnEvents(t, raw)) // cycle 1
	require.Zero(t, countConflictRows(t, raw), "cycle 1")

	setDescription(t, s, "MTIX-1", "v2")
	pullEvents(t, s, pushPendingOwnEvents(t, raw)) // cycle 2

	require.Zero(t, countConflictRows(t, raw),
		"an own event whose field has earlier events must not log a conflict")
	require.Equal(t, "v2", nodeDescription(t, raw, "MTIX-1"))
}

// S7 — an own claim replayed after the node was marked done locally.
func TestIdempotentApply_OwnClaimReplayedAfterDone_StatusStaysDone(t *testing.T) {
	ctx := context.Background()
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
	pushed := pushPendingOwnEvents(t, raw)
	require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
	statusBefore, closedBefore := nodeStatusAndClosedAt(t, raw, "MTIX-1")
	require.Equal(t, string(model.StatusDone), statusBefore)
	require.True(t, closedBefore.Valid, "done sets closed_at")

	pullEvents(t, s, pushed)

	status, closedAt := nodeStatusAndClosedAt(t, raw, "MTIX-1")
	require.Equal(t, string(model.StatusDone), status,
		"a replayed own claim must not revert the newer local done")
	require.True(t, closedAt.Valid, "a done node keeps closed_at")
	require.Equal(t, closedBefore.String, closedAt.String, "the replay leaves closed_at untouched")
}

// TestIdempotentApply_HeldEventAnyStatus_AcknowledgedNotDispatched pins the
// own-event rule for every local sync_status: the event is recorded in
// applied_events and its clocks are merged, but it is not dispatched, adds no
// sync_events row, leaves its sync_status alone and logs no conflict.
func TestIdempotentApply_HeldEventAnyStatus_AcknowledgedNotDispatched(t *testing.T) {
	tests := []struct {
		name   string
		status model.SyncStatus
	}{
		{"pending", model.SyncStatusPending},
		{"pushed", model.SyncStatusPushed},
		{"conflicted", model.SyncStatusConflicted},
		{"applied without an applied_events row", model.SyncStatusApplied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := mutationTestStore(t)
			mustCreateNode(t, s, "MTIX-1", "")
			setDescription(t, s, "MTIX-1", "emitted")
			id := latestUpdateFieldEventID(t, raw)
			_, err := raw.Exec(`UPDATE sync_events SET sync_status = ? WHERE event_id = ?`,
				string(tt.status), id)
			require.NoError(t, err)
			held := readWireEvent(t, raw, id)

			// Detectors: a dispatch would rewrite the description; a skipped
			// clock merge would leave the reset clocks behind.
			_, err = raw.Exec(`UPDATE nodes SET description = 'local-only' WHERE id = 'MTIX-1'`)
			require.NoError(t, err)
			_, err = raw.Exec(`UPDATE meta SET value = '0' WHERE key = 'meta.sync.lamport'`)
			require.NoError(t, err)
			_, err = raw.Exec(`UPDATE meta SET value = '{}' WHERE key = 'meta.sync.vector_clock'`)
			require.NoError(t, err)
			eventsBefore := countSyncEvents(t, raw)

			pullEvents(t, s, []*model.SyncEvent{held})

			require.Equal(t, 1, countRows(t, raw,
				`SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, id), "recorded in applied_events")
			require.Equal(t, "local-only", nodeDescription(t, raw, "MTIX-1"), "not dispatched")
			require.Zero(t, countConflictRows(t, raw), "no conflict row")
			require.Equal(t, eventsBefore, countSyncEvents(t, raw), "no sync_events row added")
			require.Equal(t, 1, countRows(t, raw,
				`SELECT COUNT(*) FROM sync_events WHERE event_id = ? AND sync_status = ?`,
				id, string(tt.status)), "sync_status untouched")

			lamport, ok := metaValue(t, raw, "meta.sync.lamport")
			require.True(t, ok)
			require.Equal(t, strconv.FormatInt(held.LamportClock, 10), lamport, "Lamport merged")
			vcRaw, ok := metaValue(t, raw, "meta.sync.vector_clock")
			require.True(t, ok)
			var vc model.VectorClock
			require.NoError(t, json.Unmarshal([]byte(vcRaw), &vc))
			require.Equal(t, held.VectorClock[held.AuthorID], vc[held.AuthorID], "vector clock merged")
		})
	}
}

// foreignDescriptionEvent builds a description update authored on another
// replica: its event_id is fresh, so it is not in the local log.
func foreignDescriptionEvent(t *testing.T, nodeID, value string, lamport int64) *model.SyncEvent {
	t.Helper()
	newValue, err := json.Marshal(value)
	require.NoError(t, err)
	payload, err := json.Marshal(model.UpdateFieldPayload{FieldName: "description", NewValue: newValue})
	require.NoError(t, err)
	return &model.SyncEvent{
		EventID:           clock.MustNewEventID(),
		ProjectPrefix:     "MTIX",
		NodeID:            nodeID,
		OpType:            model.OpUpdateField,
		Payload:           payload,
		WallClockTS:       1,
		LamportClock:      lamport,
		VectorClock:       model.VectorClock{"peer-b": lamport},
		AuthorID:          "peer-b",
		AuthorMachineHash: "bbbbbbbbbbbbbbbb",
	}
}

// TestIdempotentApply_ForeignFieldEvent_KeepsLWWAndConflict guards that the
// own-event rule leaves foreign events alone: they are still mirrored,
// resolved by LWW against this replica's own history, and logged as a
// conflict.
func TestIdempotentApply_ForeignFieldEvent_KeepsLWWAndConflict(t *testing.T) {
	tests := []struct {
		name        string
		lamport     int64
		wantDesc    string
		foreignWins bool
	}{
		{"newer foreign edit wins", 1000, "foreign", true},
		{"older foreign edit loses", 1, "local", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := mutationTestStore(t)
			mustCreateNode(t, s, "MTIX-1", "")
			setDescription(t, s, "MTIX-1", "local")
			pushPendingOwnEvents(t, raw)
			ownID := latestUpdateFieldEventID(t, raw)
			foreign := foreignDescriptionEvent(t, "MTIX-1", "foreign", tt.lamport)

			pullEvents(t, s, []*model.SyncEvent{foreign})

			require.Equal(t, tt.wantDesc, nodeDescription(t, raw, "MTIX-1"))
			wantWinner, wantLoser := ownID, foreign.EventID
			if tt.foreignWins {
				wantWinner, wantLoser = foreign.EventID, ownID
			}
			require.Equal(t, 1, countRows(t, raw, `
				SELECT COUNT(*) FROM sync_conflicts
				 WHERE event_id_winner = ? AND event_id_loser = ? AND field_name = 'description'`,
				wantWinner, wantLoser), "the LWW conflict is logged as before")
			require.Equal(t, 1, countConflictRows(t, raw))
			require.Equal(t, 1, countRows(t, raw,
				`SELECT COUNT(*) FROM sync_events WHERE event_id = ? AND sync_status = 'applied'`,
				foreign.EventID), "the foreign event is mirrored")
			require.Equal(t, 1, countRows(t, raw,
				`SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, foreign.EventID))
		})
	}
}

// TestIdempotentApply_HeldEventStoreFailure_ReturnsWrappedError covers the
// own-event rule's error paths: each failure surfaces, wrapped with the event
// id and the step, and the batch transaction rolls back.
func TestIdempotentApply_HeldEventStoreFailure_ReturnsWrappedError(t *testing.T) {
	tests := []struct {
		name    string
		breakDB string
		wantErr string
	}{
		{"own-event lookup fails", `ALTER TABLE sync_events RENAME TO sync_events_moved`,
			"own-event check"},
		{"local lamport unreadable", `UPDATE meta SET value = 'not-a-number' WHERE key = 'meta.sync.lamport'`,
			"held event: advance lamport"},
		{"local vector clock unreadable", `UPDATE meta SET value = '{broken' WHERE key = 'meta.sync.vector_clock'`,
			"held event: merge VC"},
		{"applied_events refuses the insert",
			`CREATE TRIGGER refuse_applied BEFORE INSERT ON applied_events BEGIN SELECT RAISE(ABORT, 'refused'); END`,
			"held event: record applied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := mutationTestStore(t)
			mustCreateNode(t, s, "MTIX-1", "")
			setDescription(t, s, "MTIX-1", "v1")
			held := readWireEvent(t, raw, latestUpdateFieldEventID(t, raw))
			_, err := raw.Exec(tt.breakDB)
			require.NoError(t, err)

			err = applyEventsInTx(s, []*model.SyncEvent{held})

			require.Error(t, err)
			require.Contains(t, err.Error(), held.EventID)
			require.Contains(t, err.Error(), tt.wantErr)
			require.Zero(t, countRows(t, raw, `SELECT COUNT(*) FROM applied_events`),
				"the failed batch leaves no applied_events row")
		})
	}
}
