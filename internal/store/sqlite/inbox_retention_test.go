// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.2: the written retention guarantee. An event addressed to an agent
// stays in that agent's inbox until the agent acks it; nothing expires it. The
// tests below pin the result of every path that could touch the journal, and
// the paths that do lose events say so in advance (UnackedInboxEvents is the
// count the warnings print).

// retentionStore is a file-backed store with a raw handle for journal edits.
func retentionStore(t *testing.T) (*sqlite.Store, *sql.DB) {
	t.Helper()
	return mutationTestStore(t)
}

// seqsOf lists the seqs of events.
func seqsOf(events []sqlite.InboxEvent) []int64 {
	out := make([]int64, 0, len(events))
	for _, e := range events {
		out = append(out, e.Seq)
	}
	return out
}

func unackedTotal(t *testing.T, s *sqlite.Store) int64 {
	t.Helper()
	n, err := s.UnackedInboxEvents(context.Background())
	require.NoError(t, err)
	return n
}

func TestInboxRetention_LongGap_UnackedEventKept(t *testing.T) {
	ctx := context.Background()
	s, raw := retentionStore(t)
	addressTo(t, s, "opus", "PROJ-1")
	addressTo(t, s, "opus", "PROJ-2")
	before, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, before, 2)

	// Ten years pass, other agents are served and acked, the store is
	// re-opened: nothing may expire or compact the addressed events.
	s.SetClock(func() time.Time { return time.Now().UTC().AddDate(10, 0, 0) })
	addressTo(t, s, "other", "PROJ-3")
	other, err := s.InboxList(ctx, "other")
	require.NoError(t, err)
	require.NoError(t, s.InboxAckThrough(ctx, "other", other[0].Seq))

	var held int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM sync_events WHERE retained_until IS NOT NULL`).Scan(&held))
	assert.Zero(t, held, "no code sets an expiry on a journal event")

	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	assert.Equal(t, seqsOf(before), seqsOf(got), "an unacked event is kept across a long gap")
	assert.Equal(t, int64(2), unackedTotal(t, s), "the other agent's acked event does not count as unacked")
}

func TestInboxRetention_ExportImportMerge_UnackedKept(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	addressTo(t, s, "opus", "PROJ-1")
	addressTo(t, s, "opus", "PROJ-2")
	listed, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.NoError(t, s.InboxAck(ctx, "opus", listed[0].Seq))

	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	_, err = s.Import(ctx, data, sqlite.ImportModeMerge, false)
	require.NoError(t, err)

	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	assert.Equal(t, []int64{listed[1].Seq}, seqsOf(got), "merge keeps the unacked event and the acked one stays acked")
}

func TestInboxRetention_ExportImportReplace_UnackedKeptOrRefused(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		dropNode string // the task the file lacks, "" for a faithful round trip
		want     []string
	}{
		{"file keeps every task: events kept", "", []string{"PROJ-1", "PROJ-2"}},
		{"file lacks a task: its events go with it, the count was shown first", "PROJ-2", []string{"PROJ-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := retentionStore(t)
			addressTo(t, s, "opus", "PROJ-1")
			addressTo(t, s, "opus", "PROJ-2")
			require.Equal(t, int64(2), unackedTotal(t, s), "the warning count covers both events")

			data, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			if tt.dropNode != "" {
				data = withoutExportNode(data, tt.dropNode)
				require.NoError(t, sqlite.RecomputeExportChecksum(data))
			}
			_, err = s.Import(ctx, data, sqlite.ImportModeReplace, false)
			require.NoError(t, err)

			got, err := s.InboxList(ctx, "opus")
			require.NoError(t, err)
			nodes := make([]string, 0, len(got))
			for _, e := range got {
				nodes = append(nodes, e.NodeID)
			}
			assert.Equal(t, tt.want, nodes)
		})
	}
}

// Into a different store the journal does not travel: tasks.json carries
// tasks and annotations, not the journal. The warning count is the reader's
// notice of that, so the source count must be exact and the target empty.
func TestInboxRetention_ExportToFreshStore_JournalNotCarried(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	addressTo(t, s, "opus", "PROJ-1")
	require.Equal(t, int64(1), unackedTotal(t, s))
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)

	fresh, _ := retentionStore(t)
	_, err = fresh.Import(ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	got, err := fresh.InboxList(ctx, "opus")
	require.NoError(t, err)
	assert.Empty(t, got, "the journal is per store: an import elsewhere carries no inbox")
	assert.Equal(t, int64(1), unackedTotal(t, s), "the source store still holds the event")
}

func TestInboxRetention_BackupRestore_UnackedKept(t *testing.T) {
	ctx := context.Background()
	s, raw := retentionStore(t)
	for _, id := range []string{"PROJ-1", "PROJ-2", "PROJ-3", "PROJ-4"} {
		addressTo(t, s, "opus", id)
	}
	listed, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, listed, 4)
	// A gap in the rowids: acks are keyed to rowid, so a backup that
	// renumbered them would shift every ack onto another event.
	_, err = raw.Exec(`DELETE FROM sync_events WHERE node_id = 'PROJ-1'`)
	require.NoError(t, err)
	require.NoError(t, s.InboxAck(ctx, "opus", listed[2].Seq))
	want, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)

	dest := filepath.Join(t.TempDir(), "backup.db")
	_, err = s.Backup(ctx, dest)
	require.NoError(t, err)
	restored, err := sqlite.New(dest, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })

	got, err := restored.InboxList(ctx, "opus")
	require.NoError(t, err)
	assert.Equal(t, seqsOf(want), seqsOf(got), "the restored store lists the same unacked events with the same seqs")
	assert.NotEmpty(t, got)
}

// A hub clone applies the pulled journal into an empty store. Acks are local
// to a store, so every addressed event is unacked there (re-delivered, never
// lost); carrying acks is MTIX-112.17.16.
func TestInboxRetention_HubClone_UnackedKept(t *testing.T) {
	ctx := context.Background()
	src, _ := retentionStore(t)
	addressTo(t, src, "opus", "PROJ-1")
	addressTo(t, src, "opus", "PROJ-2")
	listed, err := src.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.NoError(t, src.InboxAck(ctx, "opus", listed[0].Seq))
	events, err := src.ReadPendingEvents(ctx, 100)
	require.NoError(t, err)

	clone, _ := retentionStore(t)
	require.NoError(t, clone.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range events {
			if err := clone.IdempotentApply(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	}))
	got, err := clone.InboxList(ctx, "opus")
	require.NoError(t, err)
	assert.Len(t, got, 2, "both addressed events reach the clone, the previously acked one included")
}

func TestInboxRetention_DiscardLocal_CountsUnackedBeforeWipe(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	addressTo(t, s, "opus", "PROJ-1")
	addressTo(t, s, "other", "PROJ-2")
	listed, err := s.InboxList(ctx, "other")
	require.NoError(t, err)
	require.NoError(t, s.InboxAck(ctx, "other", listed[0].Seq))
	assert.Equal(t, int64(1), unackedTotal(t, s), "only the unacked event counts")

	require.NoError(t, sqlite.DiscardLocal(ctx, s, discardMtixDir(t)))
	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	assert.Empty(t, got, "discard-local wipes the journal: documented, warned with the count")
	assert.Zero(t, unackedTotal(t, s))
}

func TestInboxHistory_ListsAddressedAndAckTimes(t *testing.T) {
	ctx := context.Background()
	s, raw := retentionStore(t)
	fixed := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, id := range []string{"PROJ-1", "PROJ-2", "PROJ-3", "PROJ-4"} {
		addressTo(t, s, "opus", id)
	}
	addressTo(t, s, "other", "PROJ-5")
	_, err := raw.Exec(`UPDATE sync_events SET author_id = 'journal-author', payload = json_set(payload, '$.author_id', 'alice') WHERE node_id = 'PROJ-4'`)
	require.NoError(t, err)
	listed, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	s.SetClock(func() time.Time { return fixed })
	require.NoError(t, s.InboxAck(ctx, "opus", listed[1].Seq))        // selective: ledger row with a time
	require.NoError(t, s.InboxAckThrough(ctx, "opus", listed[0].Seq)) // watermark: no per-event time
	require.NoError(t, s.InboxAck(ctx, "opus", listed[3].Seq))
	// Another agent acking the same seq is another agent's state.
	require.NoError(t, s.InboxAck(ctx, "other", listed[2].Seq))

	hist, err := s.InboxHistory(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, hist, 4, "every event addressed to opus, none addressed to other")
	assert.Equal(t, listed[0].Seq, hist[0].Seq)
	assert.Equal(t, sqlite.InboxAckedThrough, hist[0].State)
	assert.Nil(t, hist[0].AckedAt, "ack --through keeps no per-event time")
	assert.Equal(t, sqlite.InboxAcked, hist[1].State)
	require.NotNil(t, hist[1].AckedAt)
	assert.True(t, hist[1].AckedAt.Equal(fixed))
	assert.Equal(t, sqlite.InboxUnacked, hist[2].State)
	assert.Nil(t, hist[2].AckedAt)
	assert.Equal(t, sqlite.InboxAcked, hist[3].State)
	assert.Equal(t, "PROJ-4", hist[3].NodeID)
	assert.Equal(t, "ruling", hist[3].Body)
	assert.Equal(t, "alice", hist[3].Author, "the author is the payload author, as the inbox shows it")

	_, err = s.InboxHistory(ctx, "")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	none, err := s.InboxHistory(ctx, "nobody")
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestInboxHistory_ReadOnly_DoesNotMoveAcksOrCursor(t *testing.T) {
	ctx := context.Background()
	s, raw := retentionStore(t)
	addressTo(t, s, "opus", "PROJ-1")
	_, err := s.InboxHistory(ctx, "opus")
	require.NoError(t, err)
	assert.Zero(t, countRows(t, raw, `SELECT COUNT(*) FROM agent_inbox_ack`))
	assert.Zero(t, countRows(t, raw, `SELECT COUNT(*) FROM agent_inbox_cursor`))
	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	assert.Len(t, got, 1, "reading the history leaves the event unacked")
}
