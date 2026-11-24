// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// A hub copy that has the relay event's id and payload but another node or op
// does not retire the forward: the event stays pending for push's integrity
// check (ADR-006 I6). The identical copy does retire it.
func TestAcknowledgeRelayForward_NodeAndOpMustMatch(t *testing.T) {
	for _, c := range []struct {
		name  string
		edit  func(e *model.SyncEvent)
		acked bool
	}{
		{"identical copy", func(*model.SyncEvent) {}, true},
		{"other node", func(e *model.SyncEvent) { e.NodeID = "PROJ-9" }, false},
		{"other op", func(e *model.SyncEvent) { e.OpType = model.OpUpdateField }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := t.Context()
			s := newTestStore(t)
			e := &model.SyncEvent{
				EventID: clock.MustNewEventID(), ProjectPrefix: "PROJ", NodeID: "PROJ-1", OpType: model.OpCreateNode,
				Payload: []byte(`{"title":"t"}`), WallClockTS: 1, LamportClock: 1,
				VectorClock: model.VectorClock{"p": 1}, AuthorID: "p", AuthorMachineHash: "aaaaaaaaaaaaaaaa",
			}
			e.UID = e.EventID
			require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error { return s.ApplyRelayEvent(ctx, tx, e) }))
			hub := *e
			c.edit(&hub)
			require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error { return sqlite.AcknowledgeRelayForward(ctx, tx, &hub) }))
			pending, err := s.ReadPendingEvents(ctx, 10)
			require.NoError(t, err)
			require.Equal(t, !c.acked, len(pending) == 1)
		})
	}
}

// A relay comment that upgrades a restored inbox row stays pending for the hub
// with its forward marker: the hub has never seen it.
func TestRestoredInbox_RelayUpgradeStaysPendingForHub(t *testing.T) {
	ctx := t.Context()
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	dest, raw := retentionStore(t)
	_, err := dest.Import(ctx, transportExport(t, source), sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	events, err := source.ReadPendingEvents(ctx, 100)
	require.NoError(t, err)
	var event *model.SyncEvent
	for _, e := range events {
		if e.OpType == model.OpComment {
			event = e
			break
		}
	}
	require.NotNil(t, event)
	require.NoError(t, dest.WithTx(ctx, func(tx *sql.Tx) error { return dest.ApplyRelayEvent(ctx, tx, event) }))
	var status string
	require.NoError(t, raw.QueryRow(`SELECT sync_status FROM sync_events WHERE event_id=?`, event.EventID).Scan(&status))
	require.Equal(t, "pending", status)
	forwarded, err := dest.IsRelayForward(ctx, event.EventID)
	require.NoError(t, err)
	require.True(t, forwarded)
}
