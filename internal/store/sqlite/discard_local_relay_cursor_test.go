// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-77, the relay half of the MTIX-66 defect family, and the one with a
// normative spec clause behind it.
//
// FR-21 §5.7 grounds the soundness of ordered-membership tail-verify on the
// publish cursor and the journal living in one store, so that "a restore
// rewinds cursor and journal together, atomically". DiscardLocal broke that
// invariant from the side the clause did not anticipate: it rewound the
// journal and left the cursor. Because sync_events has no AUTOINCREMENT, the
// rowid space restarts at 1 beneath a cursor still holding the old tail, and a
// publisher that had already run its once-per-run tail-verify resumes from the
// stale position and silently skips the head of the new journal.
//
// The rewind must be an UPDATE, never a DELETE: the row carries pub_epoch and
// next_rs, the publisher's identity in every frame (§5.7), and a missing row
// reports as epoch 1 / next_rs 1 — rewinding identity, not just position.

// TestDiscardLocal_RewindsRelayPushCursorPreservingFrameIdentity pins both
// halves: the position resets so nothing is skipped, and the frame identity
// survives so no (pub_epoch, rs) coordinate is ever reused.
func TestDiscardLocal_RewindsRelayPushCursorPreservingFrameIdentity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mtixDir := discardMtixDir(t)

	addressThree(t, s, "opus")
	tail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.Greater(t, tail, int64(0))

	// Publish the whole journal, then take a second epoch so the test would
	// catch a DELETE that silently rewinds the publisher to the epoch-1
	// default rather than preserving what it had reached.
	require.NoError(t, s.AdvanceRelayPushCursor(ctx, tail, uint64(tail+1)))
	require.NoError(t, s.ResetRelayPublisher(ctx, tail, uint64(tail+1)))
	before, err := s.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, tail, before.Seq)
	require.Greater(t, before.PubEpoch, uint16(1), "premise: the peer must be past the epoch-1 default")

	require.NoError(t, sqlite.DiscardLocal(ctx, s, mtixDir))

	after, err := s.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Zero(t, after.Seq,
		"the publish position must not outlive the journal it indexes into; a stale cursor silently skips the head of the new journal")
	require.Equal(t, before.PubEpoch, after.PubEpoch,
		"pub_epoch is this publisher's identity in every frame, not a position — a discard must not rewind it (a DELETE would reset it to 1 and collide with its own history)")
	require.Equal(t, before.NextRS, after.NextRS,
		"next_rs must never rewind, or re-emitted events would reuse relay coordinates readers have already consumed")
}

// TestDiscardLocal_RelayPublisherSeesEveryPostDiscardEvent is the behavioural
// half: after the wipe, the publisher's next read must cover the whole new
// journal. Under the stale cursor it began past the head and the events below
// it were never framed — a hole no reader could detect, which is precisely the
// outcome §6.2 says the advance-after-append ordering exists to prevent.
func TestDiscardLocal_RelayPublisherSeesEveryPostDiscardEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mtixDir := discardMtixDir(t)

	addressThree(t, s, "opus")
	oldTail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.NoError(t, s.AdvanceRelayPushCursor(ctx, oldTail, uint64(oldTail+1)))

	require.NoError(t, sqlite.DiscardLocal(ctx, s, mtixDir))

	// Stand in for the pull that follows a discard.
	addressTo(t, s, "opus", "PROJ-A")
	addressTo(t, s, "opus", "PROJ-B")
	newTail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.LessOrEqual(t, newTail, oldTail,
		"guard the premise: rowids must have restarted at or below the old cursor, or this proves nothing")

	pos, err := s.RelayPushCursor(ctx)
	require.NoError(t, err)
	rows, err := s.ReadRelayJournalSince(ctx, pos.Seq, 500)
	require.NoError(t, err)
	require.Len(t, rows, int(newTail),
		"every event in the post-discard journal must be publishable; a stale cursor drops the ones beneath it with no error and no log")
	require.Equal(t, int64(1), rows[0].Seq, "the read must start at the very head of the new journal")
}
