// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func TestExportWithInbox_ConcurrentWrite_SharesBoardSnapshot(t *testing.T) {
	s := exchangeSnapshotStore(t)
	require.NoError(t, s.CreateNode(t.Context(), &model.Node{ID: "TEST-1", Project: "TEST", Seq: 1, Title: "before", Status: model.StatusOpen, Priority: 3, Weight: 1}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready, written := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		select {
		case <-ready:
		case <-ctx.Done():
			finished <- ctx.Err()
			return
		}
		err := s.WithTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE nodes SET title='after' WHERE id='TEST-1'`); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES('snapshot-mail','TEST','TEST-1','comment','{"to":"worker","body":"after"}',0,1,'{}','sender','','2026-11-05T00:00:00Z')`)
			return err
		})
		close(written)
		finished <- err
	}()
	t.Cleanup(func() { cancel(); require.NoError(t, <-finished) })
	clearObserver := observeOutboundCommit(ctx, s, ready, written)
	before, err := s.ExportWithInbox(ctx, "TEST", "")
	clearObserver()
	require.NoError(t, err)
	require.Equal(t, "before", before.Nodes[0].Title)
	require.Empty(t, before.InboxJournal.Events)
	after, err := s.ExportWithInbox(ctx, "TEST", "")
	require.NoError(t, err)
	require.Equal(t, "after", after.Nodes[0].Title)
	require.Len(t, after.InboxJournal.Events, 1)
}

func TestMergeRecoveredInbox_ConflictsAndStaleCoverage_KeepsDatabaseContentAndAcks(t *testing.T) {
	e := inboxJournalEvent{EventID: "same", Author: "sender", Project: "TEST", NodeID: "TEST-1", Payload: `{"to":"worker","body":"original"}`, CreatedAt: "2026-11-05T00:00:00Z"}
	dst := &InboxJournal{Version: 1, Events: []inboxJournalEvent{e}}
	acked := e
	acked.Acked = true
	acked.AckedAt = e.CreatedAt
	extra := e
	extra.EventID = "extra"
	src := &InboxJournal{Version: 1, Events: []inboxJournalEvent{acked, extra}}
	res := &RecoverResult{}
	mergeRecoveredInbox(dst, src, res)
	require.Len(t, dst.Events, 2)
	require.True(t, dst.Events[0].Acked)
	require.Equal(t, e.CreatedAt, dst.Events[0].AckedAt)
	mergeRecoveredInbox(dst, &InboxJournal{Events: []inboxJournalEvent{e}}, res)
	require.True(t, dst.Events[0].Acked)
	e.Payload = `{"to":"other","body":"conflict"}`
	mergeRecoveredInbox(dst, &InboxJournal{Events: []inboxJournalEvent{e}}, res)
	require.Equal(t, acked, dst.Events[0])
	require.Contains(t, res.Notes[0], "conflicting")
}
