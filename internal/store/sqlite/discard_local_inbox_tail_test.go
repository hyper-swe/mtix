// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-66. DiscardLocal wipes sync_events but left the inbox bookkeeping
// keyed to it — agent_inbox_cursor, agent_inbox_ack and inbox_deliveries all
// hold sync_events ROWIDS. sync_events has no AUTOINCREMENT, so after a full
// wipe the next insert takes rowid 1 again and the surviving bookkeeping sits
// ABOVE the whole new journal: every addressed event lands at or under a stale
// watermark, or collides with a stale ack row, and InboxList hides it. The
// agent's inbox stays empty while messages are being written to it, and stays
// that way until the journal grows back past the old tail.
//
// Same silent-swallow class as the MTIX-64.2 ack-tail clamp — there the ack
// ran ahead of the journal, here the journal was reset beneath the ack.

// discardMtixDir is a .mtix directory for DiscardLocal's audit log.
func discardMtixDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

// addressTo posts one comment addressed to agent on a new node.
func addressTo(t *testing.T, s *sqlite.Store, agent, id string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode(id, "PROJ", "Task", now)))
	require.NoError(t, s.SetAnnotations(ctx, id, []model.Annotation{
		{ID: "ann" + id, Author: "worker", Text: "ruling", CreatedAt: now, Addressee: agent},
	}))
}

// TestDiscardLocal_StaleCursorDoesNotSwallowNewEvents: the cumulative
// watermark must not survive the wipe of the journal it indexes into.
func TestDiscardLocal_StaleCursorDoesNotSwallowNewEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mtixDir := discardMtixDir(t)

	_, _, s3 := addressThree(t, s, "opus")
	require.NoError(t, s.InboxAckThrough(ctx, "opus", s3))
	require.Greater(t, s3, int64(0), "the pre-discard watermark must be above the post-discard tail")

	require.NoError(t, sqlite.DiscardLocal(ctx, s, mtixDir))

	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Empty(t, got, "the journal was wiped, so there is nothing to deliver yet")

	// Everything below is written AFTER the discard: an agent that takes the
	// hub as ground truth must still receive what happens next.
	addressTo(t, s, "opus", "PROJ-A")
	got, err = s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 1,
		"an event journaled after DiscardLocal must be delivered; a cursor left above the new tail swallows it")
	require.LessOrEqual(t, got[0].Seq, s3,
		"guard the premise: the new event must reuse a rowid at or below the stale watermark, or this proves nothing")
}

// TestDiscardLocal_StaleAckLedgerDoesNotPreAckNewEvents: the selective ack
// ledger is the same defect in per-event form. A surviving (agent, seq) row
// pre-acknowledges the UNRELATED event that later takes that rowid.
func TestDiscardLocal_StaleAckLedgerDoesNotPreAckNewEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mtixDir := discardMtixDir(t)

	s1, s2, _ := addressThree(t, s, "opus")
	// Selective acks leave ledger rows WITHOUT advancing the watermark, so
	// this isolates the ledger from the cursor tested above.
	require.NoError(t, s.InboxAck(ctx, "opus", s1))
	require.NoError(t, s.InboxAck(ctx, "opus", s2))

	require.NoError(t, sqlite.DiscardLocal(ctx, s, mtixDir))

	addressTo(t, s, "opus", "PROJ-A")
	addressTo(t, s, "opus", "PROJ-B")

	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 2,
		"both post-discard events must be delivered; a stale ack ledger row silently consumes the one that reuses its rowid")
}
