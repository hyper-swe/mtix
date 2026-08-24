// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-79 / FR-21 §5.7 v1.3.6. The tail-verify produces an ATTESTATION, and an
// attestation is only valid for the journal it was taken against.
//
// §5.7 already records the adjacent form of this argument for the publish
// cursor: it "lives in the same store as the journal, so a restore rewinds
// cursor and journal together, atomically". The `verified` flag is that same
// state living OUTSIDE the store, in process memory, where nothing rewinds it.
// A publisher alive across a journal reset otherwise keeps publishing on the
// strength of an attestation about a journal that no longer exists — emitting
// a second, unrelated history into one pub_epoch, undeclared, which is the
// D-R9 outcome §5.7 exists to prevent.
//
// Two witnesses bound the cache and neither is redundant: the generation is
// exact but depends on every future author of a rowid-space reset remembering
// to bump it; tail regression needs no cooperation from any writer but has a
// narrow hole where a reset regrows past the old tail first.
package publisher_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func discardDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(d, 0o755))
	return d
}

// TestPublisher_AttestationSurvivesOrdinaryJournalGrowth guards the cache
// itself. If ordinary growth voided the attestation, the verify would re-run
// on every publish — the §6.2 on-commit trigger is why it is cached at all —
// and the two tests below would pass for a reason that has nothing to do with
// detecting a reset.
func TestPublisher_AttestationSurvivesOrdinaryJournalGrowth(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	r.journalEvents(t, 3)
	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		r.journalEvents(t, 2)
		n, pubErr := p.PublishPending(ctx)
		require.NoError(t, pubErr, "ordinary growth must never look like a reset")
		require.False(t, p.Diverged())
		require.Positive(t, n, "growth must keep publishing")
	}
}

// TestPublisher_AttestationVoidedByJournalGenerationChange is the defect.
// A publisher that verified before a discard must not keep publishing on that
// attestation afterwards; it must re-verify, find the history it published
// absent from the journal, and refuse exactly as a fresh process does.
func TestPublisher_AttestationVoidedByJournalGenerationChange(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	r.journalEvents(t, 5)
	p, err := publisher.New(r.config())
	require.NoError(t, err)
	n, err := p.PublishPending(ctx)
	require.NoError(t, err)
	require.Positive(t, n, "premise: this peer has published a history")

	genBefore, err := r.store.JournalGeneration(ctx)
	require.NoError(t, err)

	require.NoError(t, sqlite.DiscardLocal(ctx, r.store, discardDir(t)))

	genAfter, err := r.store.JournalGeneration(ctx)
	require.NoError(t, err)
	require.Greater(t, genAfter, genBefore, "the wipe must witness itself")

	// The pull that follows a discard, refilled past the old tail so the
	// tail-regression witness alone would NOT catch it. This isolates the
	// generation witness.
	r.journalEvents(t, 9)

	_, err = p.PublishPending(ctx)
	require.ErrorIs(t, err, publisher.ErrPublisherDiverged,
		"a live publisher must re-verify across a journal reset, not trust an attestation about a journal that no longer exists")
	require.True(t, p.Diverged())
}

// TestPublisher_AttestationVoidedByTailRegressionAlone pins the backstop on
// its own. It stands in for a FUTURE operation that resets the rowid space and
// forgets to bump the generation — the same by-hand discipline whose repeated
// failure produced this defect family. Without an independent test the
// backstop would rot unnoticed behind the exact witness.
func TestPublisher_AttestationVoidedByTailRegressionAlone(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	r.journalEvents(t, 6)
	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	genBefore, err := r.store.JournalGeneration(ctx)
	require.NoError(t, err)

	// Wipe the journal WITHOUT touching the generation, then refill short.
	require.NoError(t, r.wipeJournalLeavingGeneration(ctx))
	genAfter, err := r.store.JournalGeneration(ctx)
	require.NoError(t, err)
	require.Equal(t, genBefore, genAfter,
		"premise: the generation must be unchanged, or this tests the other witness")

	r.journalEvents(t, 2)

	_, err = p.PublishPending(ctx)
	require.ErrorIs(t, err, publisher.ErrPublisherDiverged,
		"a tail below the attested one can only mean the rowid space was reset; the backstop must catch a resetter that forgot the generation")
}

// TestPublisher_NeverPublishedIsNotDivergedByADiscard: the re-verify must
// re-establish the attestation, not assert a conclusion. A peer with no
// segments has published nothing that could diverge, and §5.7 warns that
// operators trained to reflexively reset-peer are their own safety hazard.
func TestPublisher_NeverPublishedIsNotDivergedByADiscard(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	r.journalEvents(t, 3)
	p, err := publisher.New(r.config())
	require.NoError(t, err)

	require.NoError(t, sqlite.DiscardLocal(ctx, r.store, discardDir(t)))
	r.journalEvents(t, 2)

	n, err := p.PublishPending(ctx)
	require.NoError(t, err, "a peer that never published cannot have diverged")
	require.False(t, p.Diverged())
	require.Positive(t, n, "and it must go on to publish the post-discard journal")
}

// TestPublisher_UnreadableWitnessIsNotTreatedAsUnchanged is the fail-secure
// clause. A witness that cannot be read is doubt, and doubt must never be
// resolved into a valid attestation — the failure that would reintroduce this
// defect the moment the meta read errored. It must also not read as a
// DIVERGENCE: that verdict summons an operator, and a database hiccup must not.
func TestPublisher_UnreadableWitnessIsNotTreatedAsUnchanged(t *testing.T) {
	boom := errors.New("database is unavailable")
	tests := []struct {
		name string
		fake *fakeJournal
	}{
		{"generation unreadable", &fakeJournal{
			pos:    sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1},
			rows:   []sqlite.RelayJournalEvent{fakeRow(1, "e1")},
			errGen: boom,
		}},
		{"tail unreadable", &fakeJournal{
			pos:     sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1},
			rows:    []sqlite.RelayJournalEvent{fakeRow(1, "e1")},
			errTail: boom,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := publisher.New(fakeConfig(t, tt.fake))
			require.NoError(t, err)

			_, err = p.PublishPending(context.Background())
			require.ErrorIs(t, err, boom, "the store failure must be reported, not swallowed")
			require.NotErrorIs(t, err, publisher.ErrPublisherDiverged,
				"an unreadable witness is a store fault, not a divergence — it must not summon an operator")
			require.False(t, p.Diverged())
			require.Zero(t, tt.fake.advances,
				"nothing may be published on the strength of a witness that could not be read")
		})
	}
}

// TestPublisher_ResetPeerFailsBeforeMutatingWhenWitnessUnreadable: the reset
// reads its witness first on purpose. A reset that succeeded but could not be
// attested would re-verify on the next publish, find the pre-reset history
// absent, and diverge again — turning the operator's one recovery into a
// no-op, with the epoch already burned. Failing first leaves the peer exactly
// as it was: still refusing, still recoverable, same epoch.
func TestPublisher_ResetPeerFailsBeforeMutatingWhenWitnessUnreadable(t *testing.T) {
	boom := errors.New("database is unavailable")
	fake := &fakeJournal{
		pos:    sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1},
		rows:   []sqlite.RelayJournalEvent{fakeRow(1, "e1")},
		errGen: boom,
	}
	p, err := publisher.New(fakeConfig(t, fake))
	require.NoError(t, err)

	err = p.ResetPeer(context.Background(), 0, 1)
	require.ErrorIs(t, err, boom)
	require.Equal(t, sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1}, fake.pos,
		"the epoch must not be burned by a reset that could not be attested")
}

// wipeJournalLeavingGeneration empties sync_events the way DiscardLocal does
// but WITHOUT the generation bump, standing in for a future rowid-space reset
// whose author did not know the witness existed.
func (r *rig) wipeJournalLeavingGeneration(ctx context.Context) error {
	db, err := sql.Open("sqlite", r.dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(ctx, `DELETE FROM sync_events`)
	return err
}
