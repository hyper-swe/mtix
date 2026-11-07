// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// FR-19.1: restored mail cannot establish project identity, even before a real event.
func TestRestoredInbox_FirstEventIdentityMatchesControl(t *testing.T) {
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	for _, mixed := range []bool{false, true} {
		var wantPrefix, wantHash string
		for _, withMail := range []bool{false, true} {
			dest := newTestStore(t)
			file := transportExport(t, source)
			if !withMail {
				file.InboxJournal = nil
			}
			_, err := dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
			require.NoError(t, err)
			if mixed {
				events, err := source.ReadPendingEvents(t.Context(), 100)
				require.NoError(t, err)
				require.NoError(t, dest.WithTx(t.Context(), func(tx *sql.Tx) error { return dest.IdempotentApply(t.Context(), tx, events[0]) }))
			}
			prefix, hash, err := dest.GetOrComputeLocalFirstEventHash(t.Context())
			require.NoError(t, err)
			if !withMail {
				wantPrefix, wantHash = prefix, hash
			}
			require.Equal(t, wantPrefix, prefix)
			require.Equal(t, wantHash, hash)
			if prefix != "" {
				require.NoError(t, sqlite.DetectDivergentHistory(prefix, hash, wantPrefix, wantHash))
			}
		}
	}
}

// FR-19.1: a real incoming event upgrades the mail envelope and fills a stale board once.
func TestRestoredInbox_RealPullAppliesStaleBoardWithoutRedelivery(t *testing.T) {
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	file := transportExport(t, source)
	for i := range file.Nodes {
		file.Nodes[i].Annotations = nil
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	dest, raw := retentionStore(t)
	_, err := dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	before, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, before, 3)
	require.NoError(t, dest.InboxAck(t.Context(), "recipient", before[0].Seq))
	events, err := source.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	for _, event := range events {
		if event.OpType != model.OpComment {
			continue
		}
		for range 2 {
			require.NoError(t, dest.WithTx(t.Context(), func(tx *sql.Tx) error { return dest.IdempotentApply(t.Context(), tx, event) }))
		}
		node, err := dest.GetNode(t.Context(), event.NodeID)
		require.NoError(t, err)
		require.Len(t, node.Annotations, 1)
		var clock int64
		var machine string
		require.NoError(t, raw.QueryRow(`SELECT lamport_clock,author_machine_hash FROM sync_events WHERE event_id=?`, event.EventID).Scan(&clock, &machine))
		require.Equal(t, event.LamportClock, clock)
		require.Equal(t, event.AuthorMachineHash, machine)
	}
	var marked int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM inbox_restored_events`).Scan(&marked))
	require.Zero(t, marked)
	after, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Equal(t, before[1:], after)
	hooks, err := dest.ReadJournalSince(t.Context(), 0, 100)
	require.NoError(t, err)
	require.Empty(t, hooks)
}

// FR-19.1: current and stale boards must apply exactly as a journal-free control.
func TestRestoredInbox_RealPullMatchesCurrentBoardControl(t *testing.T) {
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	events, err := source.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	want := map[string][]model.Annotation{}
	for _, journal := range []bool{false, true} {
		dest := newTestStore(t)
		file := transportExport(t, source)
		if !journal {
			file.InboxJournal = nil
		}
		_, err := dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
		require.NoError(t, err)
		for _, event := range events {
			if event.OpType != model.OpComment {
				continue
			}
			require.NoError(t, dest.WithTx(t.Context(), func(tx *sql.Tx) error { return dest.IdempotentApply(t.Context(), tx, event) }))
			node, err := dest.GetNode(t.Context(), event.NodeID)
			require.NoError(t, err)
			if !journal {
				want[event.NodeID] = node.Annotations
			}
			require.Equal(t, want[event.NodeID], node.Annotations)
		}
	}
}
