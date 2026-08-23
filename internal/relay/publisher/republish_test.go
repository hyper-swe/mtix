// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package publisher_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// TestRepublishFrom_ReEmitsIntoFreshSegments is FR-21 §6.8, the operator
// gap repair after a reader condemned a sealed segment.
//
// The events re-emitted are the same events; what changed is that a
// reader lost them. So they go out under FRESH relay sequences in FRESH
// segments — no existing file is touched, no consumed position is
// reused — and the duplicates are absorbed on arrival.
func TestRepublishFrom_ReEmitsIntoFreshSegments(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 5)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	before := r.publishedEvents(t)
	require.Len(t, before, 5)
	segsBefore, _, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)

	// Repair from the third record onwards.
	n, err := p.RepublishFrom(ctx, 3)
	require.NoError(t, err)
	require.Equal(t, 3, n, "records 3, 4 and 5 are re-emitted")

	after := r.publishedEvents(t)
	require.Len(t, after, 8, "the stream grew; nothing was rewritten")
	require.Equal(t, before, after[:5], "the original records are byte-identical and in place")
	require.Equal(t, before[2:], after[5:], "and the repair carries the same events again")

	segsAfter, _, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)
	require.Greater(t, len(segsAfter), len(segsBefore), "the repair opened a fresh segment")

	// Relay sequences stayed contiguous across the repair, so a reader
	// applies it without a gap.
	cursor := segment.Cursor{}
	for i, s := range segsAfter {
		res, err := segment.ScanFile(s.Path, segment.ScanOptions{
			Sealed: i < len(segsAfter)-1, Key: testKey, ExpectPeerID: testPeerID,
		})
		require.NoError(t, err)
		require.NoError(t, segment.CheckContinuity(cursor, res.Header))
		cursor = res.Cursor
	}
	require.Equal(t, uint16(1), cursor.PubEpoch, "gap repair is not a restore; the epoch never moved")
}

// TestRepublishFrom_UnknownRelaySequence refuses a repair the publisher
// cannot locate rather than guessing a floor — a wrong floor either
// re-sends nothing useful or re-sends the whole history.
func TestRepublishFrom_UnknownRelaySequence(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 2)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	_, err = p.RepublishFrom(ctx, 99)
	require.Error(t, err)
	require.Contains(t, err.Error(), "99")

	t.Run("and a zero relay sequence is not a position", func(t *testing.T) {
		_, err := p.RepublishFrom(ctx, 0)
		require.Error(t, err)
	})
}

// TestRepublishFrom_OnAPeerThatNeverPublished has nothing to repair.
func TestRepublishFrom_OnAPeerThatNeverPublished(t *testing.T) {
	r := newRig(t)
	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.RepublishFrom(context.Background(), 1)
	require.Error(t, err)
}

// TestRepublishFrom_FromTheVeryFirstRecord re-sends the whole stream,
// which is the recovery for a reader that lost everything.
func TestRepublishFrom_FromTheVeryFirstRecord(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 3)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	n, err := p.RepublishFrom(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.Len(t, r.publishedEvents(t), 6)
}

// TestRepublishFrom_SurfacesFailures walks the ways a repair cannot
// proceed. Each refuses rather than guessing a floor: a wrong floor
// either re-sends nothing useful or re-sends the whole history, and
// both are worse than telling the operator the repair could not run.
func TestRepublishFrom_SurfacesFailures(t *testing.T) {
	boom := errors.New("database is unavailable")
	ctx := context.Background()

	t.Run("a symlink in the peer's own directory", func(t *testing.T) {
		r := newRig(t)
		r.journalEvents(t, 1)
		p, err := publisher.New(r.config())
		require.NoError(t, err)
		_, err = p.PublishPending(ctx)
		require.NoError(t, err)

		require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(r.segDir, segment.FileName(9))))
		_, err = p.RepublishFrom(ctx, 1)
		require.ErrorIs(t, err, segment.ErrSymlink)
	})

	t.Run("the journal no longer holds the event", func(t *testing.T) {
		r := newRig(t)
		r.journalEvents(t, 2)
		p, err := publisher.New(r.config())
		require.NoError(t, err)
		_, err = p.PublishPending(ctx)
		require.NoError(t, err)

		cfg := r.config()
		cfg.Journal = &fakeJournal{seqs: map[string]int64{}}
		p2, err := publisher.New(cfg)
		require.NoError(t, err)
		_, err = p2.RepublishFrom(ctx, 1)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no longer in the journal")
	})

	t.Run("the journal lookup fails", func(t *testing.T) {
		r := newRig(t)
		r.journalEvents(t, 2)
		p, err := publisher.New(r.config())
		require.NoError(t, err)
		_, err = p.PublishPending(ctx)
		require.NoError(t, err)

		cfg := r.config()
		cfg.Journal = &fakeJournal{errLookup: boom}
		p2, err := publisher.New(cfg)
		require.NoError(t, err)
		_, err = p2.RepublishFrom(ctx, 1)
		require.ErrorIs(t, err, boom)
	})

	t.Run("the rewind itself fails", func(t *testing.T) {
		r := newRig(t)
		r.journalEvents(t, 2)
		p, err := publisher.New(r.config())
		require.NoError(t, err)
		_, err = p.PublishPending(ctx)
		require.NoError(t, err)

		ids := r.journalIDs(t)
		cfg := r.config()
		cfg.Journal = &fakeJournal{
			seqs:         map[string]int64{ids[0]: 1, ids[1]: 2},
			errRepublish: boom,
		}
		p2, err := publisher.New(cfg)
		require.NoError(t, err)
		_, err = p2.RepublishFrom(ctx, 1)
		require.ErrorIs(t, err, boom)
	})
}

// TestRepublishFrom_SkipsAnUnreadableSegmentWhileSearching keeps a
// repair possible when one segment is already damaged — which is, after
// all, the situation that prompted the repair.
func TestRepublishFrom_SkipsAnUnreadableSegmentWhileSearching(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 2)
	ctx := context.Background()

	cfg := r.config()
	cfg.MaxSegmentBytes = segment.HeaderSize + 1 // one record per segment
	p, err := publisher.New(cfg)
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	// Damage the FIRST segment; the record being repaired is in the second.
	segs, _, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(segs), 2)
	raw, err := os.ReadFile(segs[0].Path)
	require.NoError(t, err)
	raw[segment.HeaderSize+20] ^= 0x01
	require.NoError(t, os.WriteFile(segs[0].Path, raw, 0o600))

	n, err := p.RepublishFrom(ctx, 2)
	require.NoError(t, err, "a damaged segment must not block repairing a record in another")
	require.Positive(t, n)
}
