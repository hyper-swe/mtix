// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// copyJournalReversed copies every sync_events row of src into dst in reverse
// rowid order, so each event keeps its id but gets a different seq: the shape
// of a hub clone of the same history (MTIX-112.17.16).
func copyJournalReversed(t *testing.T, src, dst *sql.DB) {
	t.Helper()
	rows, err := src.Query(`SELECT * FROM sync_events ORDER BY rowid DESC`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	require.NoError(t, err)
	stmt := `INSERT INTO sync_events (` + strings.Join(cols, ",") + `) VALUES (?` + strings.Repeat(",?", len(cols)-1) + `)`
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		_, err = dst.Exec(stmt, vals...)
		require.NoError(t, err)
	}
	require.NoError(t, rows.Err())
}

// carryFixture builds an old store with three events addressed to opus: e1
// acked through the watermark, e2 unacked, e3 acked individually; and a new
// store holding the same events in reverse order (different seqs).
func carryFixture(t *testing.T) (oldS, newS *sqlite.Store, ids [3]string) {
	t.Helper()
	ctx := context.Background()
	oldS, oldRaw := retentionStore(t)
	newS, newRaw := retentionStore(t)
	addressTo(t, oldS, "opus", "PROJ-1")
	addressTo(t, oldS, "opus", "PROJ-2")
	addressTo(t, oldS, "opus", "PROJ-3")
	evs, err := oldS.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, evs, 3)
	for i := range ids {
		ids[i] = evs[i].EventID
	}
	require.NoError(t, oldS.InboxAckThrough(ctx, "opus", evs[0].Seq))
	require.NoError(t, oldS.InboxAck(ctx, "opus", evs[2].Seq))
	copyJournalReversed(t, oldRaw, newRaw)
	return oldS, newS, ids
}

func TestInboxCarry_AckedByEventID_NeverBySeq(t *testing.T) {
	ctx := context.Background()
	oldS, newS, ids := carryFixture(t)

	carry, err := oldS.ExportInboxCarry(ctx)
	require.NoError(t, err)
	got := map[string]bool{}
	for _, a := range carry.Acks {
		assert.Equal(t, "opus", a.AgentID)
		got[a.EventID] = true
	}
	assert.Equal(t, map[string]bool{ids[0]: true, ids[2]: true}, got,
		"the watermark-covered and the individually acked events are carried; the unacked one is not")

	res, err := newS.ImportInboxCarry(ctx, carry)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Acks)
	assert.Zero(t, res.UnmatchedAcks)

	// Only e2 is still in the inbox, though every seq differs from the old store.
	left, err := newS.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, left, 1)
	assert.Equal(t, ids[1], left[0].EventID, "the unacked event must be delivered again")
}

func TestInboxCarry_ReusedSeqs_DoNotAckOtherEvents(t *testing.T) {
	ctx := context.Background()
	oldS, newS, ids := carryFixture(t)
	carry, err := oldS.ExportInboxCarry(ctx)
	require.NoError(t, err)
	_, err = newS.ImportInboxCarry(ctx, carry)
	require.NoError(t, err)

	hist, err := newS.InboxHistory(ctx, "opus")
	require.NoError(t, err)
	state := map[string]sqlite.InboxState{}
	for _, h := range hist {
		state[h.EventID] = h.State
	}
	assert.Equal(t, sqlite.InboxAcked, state[ids[0]])
	assert.Equal(t, sqlite.InboxUnacked, state[ids[1]], "e2 holds the seq that e1 had in the old store; it must stay unacked")
	assert.Equal(t, sqlite.InboxAcked, state[ids[2]])
}

func TestInboxCarry_UnknownAckedEvent_CountedNotInvented(t *testing.T) {
	ctx := context.Background()
	oldS, _, _ := carryFixture(t)
	emptyS, _ := retentionStore(t)
	carry, err := oldS.ExportInboxCarry(ctx)
	require.NoError(t, err)
	res, err := emptyS.ImportInboxCarry(ctx, carry)
	require.NoError(t, err)
	assert.Equal(t, 2, res.UnmatchedAcks)
	assert.Zero(t, res.Acks)
}

func TestInboxCarry_DeliveryWithoutEvent_RefusesAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	oldS, oldRaw := retentionStore(t)
	addressTo(t, oldS, "opus", "PROJ-1")
	evs, err := oldS.InboxList(ctx, "opus")
	require.NoError(t, err)
	_, err = oldRaw.Exec(`INSERT INTO inbox_deliveries(agent_id,event_seq,hook_name,delivered_at) VALUES('opus',?,'h','2026-01-01T00:00:00Z')`, evs[0].Seq)
	require.NoError(t, err)
	carry, err := oldS.ExportInboxCarry(ctx)
	require.NoError(t, err)
	require.Len(t, carry.Deliveries, 1)

	emptyS, emptyRaw := retentionStore(t)
	_, err = emptyS.ImportInboxCarry(ctx, carry)
	require.ErrorIs(t, err, sqlite.ErrCarryEventMissing)
	var n int
	require.NoError(t, emptyRaw.QueryRow(`SELECT COUNT(*) FROM inbox_deliveries`).Scan(&n))
	assert.Zero(t, n)
}

func TestInboxCarry_UnackedDelivery_StaysDeliverable(t *testing.T) {
	ctx := context.Background()
	oldS, oldRaw := retentionStore(t)
	newS, newRaw := retentionStore(t)
	addressTo(t, oldS, "opus", "PROJ-1")
	evs, err := oldS.InboxList(ctx, "opus")
	require.NoError(t, err)
	_, err = oldRaw.Exec(`INSERT INTO inbox_deliveries(agent_id,event_seq,hook_name,delivered_at) VALUES('hook-agent',?,'h','2026-01-01T00:00:00Z')`, evs[0].Seq)
	require.NoError(t, err)
	copyJournalReversed(t, oldRaw, newRaw)
	carry, err := oldS.ExportInboxCarry(ctx)
	require.NoError(t, err)
	res, err := newS.ImportInboxCarry(ctx, carry)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Deliveries)
	left, err := newS.InboxList(ctx, "hook-agent")
	require.NoError(t, err)
	require.Len(t, left, 1, "an unacked hook delivery must survive the carry")
}

func TestLocalEvents_ListsStatusNodeOpAndTime(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	addressTo(t, s, "opus", "PROJ-1")
	evs, err := s.LocalEvents(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, evs)
	for _, e := range evs {
		assert.NotEmpty(t, e.ID)
		assert.NotEmpty(t, e.Op)
		assert.Equal(t, "pending", e.Status)
		assert.False(t, e.At.IsZero())
	}
}

// TestInboxCarry_SingleAckAtReusedSeq_AcksOnlyThatEvent uses an asymmetric
// shape: one ack at old seq 1, and a clone that reverses the journal, so event
// three holds seq 1 in the clone. A carry keyed by seq would ack event three.
func TestInboxCarry_SingleAckAtReusedSeq_AcksOnlyThatEvent(t *testing.T) {
	ctx := context.Background()
	oldS, oldRaw := retentionStore(t)
	newS, newRaw := retentionStore(t)
	addressTo(t, oldS, "opus", "PROJ-1")
	addressTo(t, oldS, "opus", "PROJ-2")
	addressTo(t, oldS, "opus", "PROJ-3")
	evs, err := oldS.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, evs, 3)
	require.NoError(t, oldS.InboxAck(ctx, "opus", evs[0].Seq))
	copyJournalReversed(t, oldRaw, newRaw)

	carry, err := oldS.ExportInboxCarry(ctx)
	require.NoError(t, err)
	_, err = newS.ImportInboxCarry(ctx, carry)
	require.NoError(t, err)

	left, err := newS.InboxList(ctx, "opus")
	require.NoError(t, err)
	var leftIDs []string
	for _, e := range left {
		leftIDs = append(leftIDs, e.EventID)
	}
	assert.ElementsMatch(t, []string{evs[1].EventID, evs[2].EventID}, leftIDs,
		"only the acked event leaves the inbox; the event that now holds the old seq is still delivered")
}

func TestInboxCarry_AckTime_RoundTripsAndBadTimeIsAnError(t *testing.T) {
	ctx := context.Background()
	oldS, oldRaw := retentionStore(t)
	newS, newRaw := retentionStore(t)
	addressTo(t, oldS, "opus", "PROJ-1")
	evs, err := oldS.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.NoError(t, oldS.InboxAck(ctx, "opus", evs[0].Seq))
	_, err = oldRaw.Exec(`UPDATE agent_inbox_ack SET acked_at = '2026-01-02T03:04:05.5Z'`)
	require.NoError(t, err)
	copyJournalReversed(t, oldRaw, newRaw)

	carry, err := oldS.ExportInboxCarry(ctx)
	require.NoError(t, err)
	_, err = newS.ImportInboxCarry(ctx, carry)
	require.NoError(t, err)
	var at string
	require.NoError(t, newRaw.QueryRow(`SELECT acked_at FROM agent_inbox_ack`).Scan(&at))
	assert.Equal(t, "2026-01-02T03:04:05.5Z", at, "the ack keeps its own time, not the import time")

	_, err = oldRaw.Exec(`UPDATE agent_inbox_ack SET acked_at = 'garbage'`)
	require.NoError(t, err)
	_, err = oldS.ExportInboxCarry(ctx)
	require.ErrorContains(t, err, "acked_at")
}

func TestLocalEvents_BadCreatedAt_IsAnError(t *testing.T) {
	s, raw := retentionStore(t)
	addressTo(t, s, "opus", "PROJ-1")
	_, err := raw.Exec(`UPDATE sync_events SET created_at = 'garbage'`)
	require.NoError(t, err)
	_, err = s.LocalEvents(context.Background())
	require.ErrorContains(t, err, "created_at")
}
