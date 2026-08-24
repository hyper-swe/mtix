// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package publisher_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

const testPeerID = "0123456789abcdef"

var testKey = []byte("fr21-publisher-fixed-test-key-32b")

// rig is a store plus the relay directory its publisher writes into.
type rig struct {
	store   *sqlite.Store
	dbPath  string
	segDir  string
	created int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "store.db")
	st, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	segDir := filepath.Join(root, "relay", "peers", testPeerID, "segments")
	require.NoError(t, os.MkdirAll(segDir, 0o700))
	return &rig{store: st, dbPath: dbPath, segDir: segDir}
}

// journalEvents creates n nodes, which journals n create_node events.
func (r *rig) journalEvents(t *testing.T, n int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < n; i++ {
		r.created++
		id := fmt.Sprintf("PROJ-%d", r.created)
		require.NoError(t, r.store.CreateNode(ctx, &model.Node{
			ID: id, Project: "PROJ", Depth: 0, Seq: r.created,
			Title: "n" + id, Status: model.StatusOpen, Priority: model.PriorityMedium,
			Weight: 1.0, NodeType: model.NodeTypeStory, Creator: "test-agent",
			ContentHash: model.ComputeContentHash("n"+id, "", "", "", nil),
			CreatedAt:   now, UpdatedAt: now,
		}))
	}
}

func (r *rig) config() publisher.Config {
	return publisher.Config{
		Journal:     r.store,
		SegmentsDir: r.segDir,
		PeerID:      testPeerID,
		Key:         testKey,
		KeyEpoch:    1,
		Logger:      slog.Default(),
	}
}

// publishedEvents scans every segment and returns the event ids in
// stream order, which is what a reader on the far side would apply.
func (r *rig) publishedEvents(t *testing.T) []string {
	t.Helper()
	segs, foreign, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)
	require.Empty(t, foreign)

	var ids []string
	for i, s := range segs {
		res, err := segment.ScanFile(s.Path, segment.ScanOptions{
			Sealed: i < len(segs)-1, Key: testKey, ExpectPeerID: testPeerID,
		})
		require.NoError(t, err, "segment %d", s.No)
		for _, rec := range res.Records {
			var e struct {
				EventID string `json:"event_id"`
			}
			require.NoError(t, json.Unmarshal(rec.Payload, &e))
			ids = append(ids, e.EventID)
		}
	}
	return ids
}

// journalIDs returns every event id in the store's journal, in order.
func (r *rig) journalIDs(t *testing.T) []string {
	t.Helper()
	rows, err := r.store.ReadRelayJournalSince(context.Background(), 0, 1000)
	require.NoError(t, err)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.Event.EventID)
	}
	return ids
}

// TestPublishPending_FramesTheJournalAndAdvancesTheCursor is the FR-21
// §6.2 happy path.
func TestPublishPending_FramesTheJournalAndAdvancesTheCursor(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 3)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)

	n, err := p.PublishPending(ctx)
	require.NoError(t, err)
	require.Equal(t, len(r.journalIDs(t)), n)
	require.Equal(t, r.journalIDs(t), r.publishedEvents(t))

	pos, err := r.store.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Positive(t, pos.Seq)
	require.Equal(t, uint64(n+1), pos.NextRS)

	t.Run("a second pass with nothing new publishes nothing", func(t *testing.T) {
		again, err := p.PublishPending(ctx)
		require.NoError(t, err)
		require.Zero(t, again)
		require.Equal(t, r.journalIDs(t), r.publishedEvents(t))
	})

	t.Run("new events append to the same stream", func(t *testing.T) {
		r.journalEvents(t, 2)
		more, err := p.PublishPending(ctx)
		require.NoError(t, err)
		require.Equal(t, 2, more)
		require.Equal(t, r.journalIDs(t), r.publishedEvents(t))
	})
}

// TestPublishPending_EmptyJournalIsNotAnError covers a fresh store.
func TestPublishPending_EmptyJournalIsNotAnError(t *testing.T) {
	r := newRig(t)
	p, err := publisher.New(r.config())
	require.NoError(t, err)

	n, err := p.PublishPending(context.Background())
	require.NoError(t, err)
	require.Zero(t, n)
}

// TestPublishPending_CursorAdvancesOnlyAfterTheAppend is the §6.2
// crash-window contract, observed from the other side: the medium is
// never behind the cursor. A cursor ahead of the medium would mean
// events were marked published that no reader can ever receive — a
// silent hole. The reverse (medium ahead of cursor) is the benign crash
// window, and republishing is absorbed downstream.
func TestPublishPending_CursorAdvancesOnlyAfterTheAppend(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 4)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	pos, err := r.store.RelayPushCursor(ctx)
	require.NoError(t, err)
	rows, err := r.store.ReadRelayJournalSince(ctx, 0, 1000)
	require.NoError(t, err)

	published := r.publishedEvents(t)
	var atOrBelowCursor []string
	for _, row := range rows {
		if row.Seq <= pos.Seq {
			atOrBelowCursor = append(atOrBelowCursor, row.Event.EventID)
		}
	}
	require.Subset(t, published, atOrBelowCursor,
		"everything the cursor claims published must actually be on the medium")
}

// TestPublishPending_RotatesAndKeepsTheStreamContiguous exercises the
// segment boundary from the publish path, and checks the contiguity a
// reader will enforce across it.
func TestPublishPending_RotatesAndKeepsTheStreamContiguous(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 12)
	cfg := r.config()
	cfg.MaxSegmentBytes = 1024

	p, err := publisher.New(cfg)
	require.NoError(t, err)
	_, err = p.PublishPending(context.Background())
	require.NoError(t, err)

	segs, _, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)
	require.Greater(t, len(segs), 1, "the stream must span several segments")

	cursor := segment.Cursor{}
	for i, s := range segs {
		res, err := segment.ScanFile(s.Path, segment.ScanOptions{
			Sealed: i < len(segs)-1, Key: testKey, ExpectPeerID: testPeerID,
		})
		require.NoError(t, err)
		require.NoError(t, segment.CheckContinuity(cursor, res.Header),
			"contiguity broke entering segment %d", s.No)
		cursor = res.Cursor
	}
	require.Equal(t, r.journalIDs(t), r.publishedEvents(t))
}

// TestOnCommit_NeverFailsTheMutation is the §6.2 contract that matters
// most at the call site: the on-commit trigger is best-effort. A relay
// that is unreachable, full, or refusing must not turn a user's
// successful mutation into a failure — a dead relay is degraded
// transport, not a damaged store.
func TestOnCommit_NeverFailsTheMutation(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 2)

	cfg := r.config()
	cfg.SegmentsDir = filepath.Join(t.TempDir(), "gone")
	p, err := publisher.New(cfg)
	require.NoError(t, err)

	require.NotPanics(t, p.OnCommit(), "an unreachable relay must not panic the mutation path")
	require.Positive(t, p.Stats().Failures, "the failure is counted for status")
}

// TestOnCommit_DoesNotReenter is the re-entrancy guard. The handler runs
// inside the store's post-commit callback, and its own cursor advance
// commits — without the guard that write would re-enter the same
// handler and recurse until the stack gave out.
func TestOnCommit_DoesNotReenter(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 3)

	p, err := publisher.New(r.config())
	require.NoError(t, err)

	// Registering on the store means every cursor advance fires the
	// handler again; it must terminate.
	r.store.AddOnCommit(p.OnCommit())
	require.NotPanics(t, func() { r.journalEvents(t, 1) })
	require.Equal(t, r.journalIDs(t), r.publishedEvents(t))
}

// TestPublishPending_RecoversFromATornTail is §5.5 driven from the
// publish path: a fresh publisher meets its own crash-damaged tail,
// seals it by rotating past it, and carries on.
func TestPublishPending_RecoversFromATornTail(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 2)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	// A crash mid-append leaves a partial frame behind.
	segs, _, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)
	f, err := os.OpenFile(segs[len(segs)-1].Path, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = f.Write([]byte("MREC\x00\x00\x00\x09partial"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	r.journalEvents(t, 2)
	p2, err := publisher.New(r.config())
	require.NoError(t, err)
	n, err := p2.PublishPending(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	after, _, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)
	require.Greater(t, len(after), len(segs), "recovery rotates past the damage")
}

// TestPublisherRestore_TailVerifyRefusesDivergence is the FR-21 §5.7
// detection half, and the first of the four G1 scenario tests.
//
// A publisher restored from backup has a journal that no longer contains
// what it already published. Continuing would emit different events
// under relay sequences readers have already consumed, and the readers'
// monotonic watermark — correct against attackers — would silently
// discard the restored peer's new work. So publishing refuses, loudly,
// naming its recovery. Reads and ingest are untouched.
func TestPublisherRestore_TailVerifyRefusesDivergence(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 3)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)
	beforeRestore := r.publishedEvents(t)
	require.Len(t, beforeRestore, 3)

	// The restore: the store comes back holding a different history,
	// while the medium still carries what the old one published.
	restored := newRigAt(t, r.segDir)
	restored.journalEvents(t, 2)

	p2, err := publisher.New(restored.config())
	require.NoError(t, err)
	n, err := p2.PublishPending(ctx)
	require.ErrorIs(t, err, publisher.ErrPublisherDiverged)
	require.Equal(t, "RELAY_PUBLISHER_DIVERGED", publisher.CodeOf(err))
	require.Contains(t, err.Error(), "reset-peer", "a refusal must name its recovery")
	require.Zero(t, n)

	require.Equal(t, beforeRestore, r.publishedEvents(t),
		"a refused publish writes nothing")
	require.True(t, p2.Diverged())

	t.Run("the refusal is sticky until the operator acts", func(t *testing.T) {
		_, err := p2.PublishPending(ctx)
		require.ErrorIs(t, err, publisher.ErrPublisherDiverged)
	})
}

// TestPublisherRestore_ResetPeerBumpsEpochAndRepublishes is the §5.7
// operator-gated recovery, and the second G1 scenario test.
func TestPublisherRestore_ResetPeerBumpsEpochAndRepublishes(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 3)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	restored := newRigAt(t, r.segDir)
	restored.journalEvents(t, 2)
	p2, err := publisher.New(restored.config())
	require.NoError(t, err)
	_, err = p2.PublishPending(ctx)
	require.ErrorIs(t, err, publisher.ErrPublisherDiverged)

	// The operator resets: a new epoch, a declared base, republish from
	// a safe floor.
	require.NoError(t, p2.ResetPeer(ctx, 0, 1))
	require.False(t, p2.Diverged(), "the reset clears the refusal")

	n, err := p2.PublishPending(ctx)
	require.NoError(t, err)
	require.Equal(t, len(restored.journalIDs(t)), n)

	pos, err := restored.store.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, uint16(2), pos.PubEpoch, "the epoch is bumped, never reused")

	segs, _, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)
	last, err := segment.ScanFile(segs[len(segs)-1].Path, segment.ScanOptions{
		Key: testKey, ExpectPeerID: testPeerID,
	})
	require.NoError(t, err)
	require.Equal(t, uint16(2), last.Header.PubEpoch, "post-restore frames carry the new epoch")
}

// TestPublisherRestore_ReadersDeliverEveryPostRestoreEvent is the third
// G1 scenario test, and the one that states the actual safety property:
// after the reset, every event the restored peer produced reaches a
// reader. Nothing is silently dropped.
func TestPublisherRestore_ReadersDeliverEveryPostRestoreEvent(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 3)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	restored := newRigAt(t, r.segDir)
	restored.journalEvents(t, 4)
	p2, err := publisher.New(restored.config())
	require.NoError(t, err)
	_, err = p2.PublishPending(ctx)
	require.ErrorIs(t, err, publisher.ErrPublisherDiverged)

	require.NoError(t, p2.ResetPeer(ctx, 0, 1))
	_, err = p2.PublishPending(ctx)
	require.NoError(t, err)

	// A reader walking the peer's whole directory sees every
	// post-restore event.
	delivered := r.publishedEvents(t)
	for _, want := range restored.journalIDs(t) {
		require.Contains(t, delivered, want,
			"post-restore event %s never reached a reader — the D-R9 worst outcome", want)
	}

	// And the new epoch's records are contiguous among themselves, so a
	// reader keying on (pub_epoch, rs) applies them without a gap.
	segs, _, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)
	var prev *segment.Header
	for i, s := range segs {
		res, err := segment.ScanFile(s.Path, segment.ScanOptions{
			Sealed: i < len(segs)-1, Key: testKey, ExpectPeerID: testPeerID,
		})
		require.NoError(t, err)
		if res.Header.PubEpoch != 2 {
			continue
		}
		if prev != nil {
			require.NoError(t, segment.CheckContinuity(
				segment.Cursor{SegmentNo: prev.SegmentNo, RS: res.Header.FirstRS - 1, PubEpoch: 2},
				res.Header))
		}
		h := res.Header
		prev = &h
	}
}

// TestPublisherRestore_IdenticalRepublishIsAbsorbedNotRefused is the
// fourth G1 scenario test, and the regression guard against a
// tail-verify that cries wolf.
//
// A restore that lands on the same history — or the ordinary crash
// between the append and the cursor advance — republishes identical
// events. That is harmless by construction (apply is idempotent), so it
// must resume normally. A tail-verify that refused here would make every
// crash an operator ticket.
func TestPublisherRestore_IdenticalRepublishIsAbsorbedNotRefused(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 4)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)
	published := r.publishedEvents(t)

	t.Run("the crash window: the cursor is rewound, the medium is ahead", func(t *testing.T) {
		// Exactly the §6.2 crash between append and cursor-advance.
		require.NoError(t, r.store.ResetRelayPublisher(ctx, 0, 1))

		p2, err := publisher.New(r.config())
		require.NoError(t, err)
		n, err := p2.PublishPending(ctx)
		require.NoError(t, err, "an identical republish must resume, not refuse")
		require.False(t, p2.Diverged())
		require.Equal(t, len(published), n)
	})

	t.Run("a fresh publisher on an unchanged store resumes", func(t *testing.T) {
		p3, err := publisher.New(r.config())
		require.NoError(t, err)
		_, err = p3.PublishPending(ctx)
		require.NoError(t, err)
		require.False(t, p3.Diverged())
	})
}

// newRigAt builds a store whose relay directory is an EXISTING one —
// the shape a restore leaves behind: a different journal pointed at a
// medium that already carries another history's records.
func newRigAt(t *testing.T, segDir string) *rig {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "restored.db")
	st, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return &rig{store: st, dbPath: dbPath, segDir: segDir}
}

// TestPublisher_ConfigValidation refuses an unusable publisher up front.
func TestPublisher_ConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*publisher.Config)
	}{
		{"no journal", func(c *publisher.Config) { c.Journal = nil }},
		{"peer id off grammar", func(c *publisher.Config) { c.PeerID = "NOPE" }},
		{"authenticated without a key", func(c *publisher.Config) { c.Key = nil }},
		{"key with the unauthenticated mode", func(c *publisher.Config) { c.Unauthenticated = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			cfg := r.config()
			tt.mutate(&cfg)
			_, err := publisher.New(cfg)
			require.Error(t, err)
		})
	}
}

// TestPublishPending_ReportsAMediumFailureWithoutLosingPosition is the
// §6.2 failure contract: a publish that cannot reach the medium is
// reported and counted, and the cursor stays put so the next tick
// retries the same events.
func TestPublishPending_ReportsAMediumFailureWithoutLosingPosition(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 2)
	ctx := context.Background()

	cfg := r.config()
	cfg.SegmentsDir = filepath.Join(t.TempDir(), "absent")
	p, err := publisher.New(cfg)
	require.NoError(t, err)

	_, err = p.PublishPending(ctx)
	require.Error(t, err)
	require.NotErrorIs(t, err, publisher.ErrPublisherDiverged,
		"an unreachable medium is not a divergence")

	pos, err := r.store.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Zero(t, pos.Seq, "a failed publish leaves the cursor where it was")
	require.Equal(t, int64(1), p.Stats().Failures)
}

// TestPublishPending_BatchLimitBoundsATick keeps a tick's work
// proportional to what is new rather than to the whole journal.
func TestPublishPending_BatchLimitBoundsATick(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 6)
	ctx := context.Background()

	cfg := r.config()
	cfg.BatchLimit = 2
	p, err := publisher.New(cfg)
	require.NoError(t, err)

	n, err := p.PublishPending(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	// Repeated ticks drain the rest without ever rereading history.
	total := n
	for i := 0; i < 5 && total < len(r.journalIDs(t)); i++ {
		more, err := p.PublishPending(ctx)
		require.NoError(t, err)
		total += more
	}
	require.Equal(t, r.journalIDs(t), r.publishedEvents(t))
}

// TestPublishPending_UnauthenticatedRelay covers the §8.3 mode from the
// publish path.
func TestPublishPending_UnauthenticatedRelay(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 2)

	cfg := r.config()
	cfg.Key = nil
	cfg.Unauthenticated = true
	p, err := publisher.New(cfg)
	require.NoError(t, err)

	n, err := p.PublishPending(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, n)

	segs, _, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)
	res, err := segment.ScanFile(segs[0].Path, segment.ScanOptions{ExpectPeerID: testPeerID})
	require.NoError(t, err)
	require.False(t, res.Header.Authenticated())
	require.Len(t, res.Records, 2)
}

// TestPublishPending_PayloadIsTheEventVerbatim pins what a record
// carries. The reader on the far side runs the same validation envelope
// a hub would, so the payload has to be the whole event, not a summary.
func TestPublishPending_PayloadIsTheEventVerbatim(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 1)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	segs, _, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)
	res, err := segment.ScanFile(segs[0].Path, segment.ScanOptions{
		Key: testKey, ExpectPeerID: testPeerID,
	})
	require.NoError(t, err)
	require.Len(t, res.Records, 1)

	var got model.SyncEvent
	require.NoError(t, json.Unmarshal(res.Records[0].Payload, &got))

	rows, err := r.store.ReadRelayJournalSince(ctx, 0, 10)
	require.NoError(t, err)
	want := rows[0].Event
	require.Equal(t, want.EventID, got.EventID)
	require.Equal(t, want.ProjectPrefix, got.ProjectPrefix)
	require.Equal(t, want.NodeID, got.NodeID)
	require.Equal(t, want.OpType, got.OpType)
	require.Equal(t, want.LamportClock, got.LamportClock)
	require.Equal(t, want.AuthorID, got.AuthorID)
	require.Equal(t, want.AuthorMachineHash, got.AuthorMachineHash)
	require.True(t, want.VectorClock.Equal(got.VectorClock))
	require.JSONEq(t, string(want.Payload), string(got.Payload))
}

// TestResetPeer_RefusesAnInvalidBase keeps the declared restart inside
// the format.
func TestResetPeer_RefusesAnInvalidBase(t *testing.T) {
	r := newRig(t)
	p, err := publisher.New(r.config())
	require.NoError(t, err)
	require.Error(t, p.ResetPeer(context.Background(), 0, 0))
}

// fakeJournal drives the publisher's error paths. The narrow Journal
// interface exists precisely so these can be exercised without a
// half-broken database.
type fakeJournal struct {
	pos          sqlite.RelayPushPosition
	rows         []sqlite.RelayJournalEvent
	seqs         map[string]int64
	errCursor    error
	errRead      error
	errAdvance   error
	errReset     error
	errLookup    error
	errRepublish error
	advances     int

	// gen/errGen and errTail drive the §5.7 v1.3.6 attestation
	// witnesses, including the fail-secure path where one is unreadable.
	gen     int64
	errGen  error
	errTail error
}

func (f *fakeJournal) JournalGeneration(context.Context) (int64, error) {
	return f.gen, f.errGen
}

func (f *fakeJournal) JournalTail(context.Context) (int64, error) {
	if f.errTail != nil {
		return 0, f.errTail
	}
	var tail int64
	for _, r := range f.rows {
		if r.Seq > tail {
			tail = r.Seq
		}
	}
	return tail, nil
}

func (f *fakeJournal) RelayPushCursor(context.Context) (sqlite.RelayPushPosition, error) {
	return f.pos, f.errCursor
}

func (f *fakeJournal) AdvanceRelayPushCursor(_ context.Context, seq int64, nextRS uint64) error {
	f.advances++
	if f.errAdvance != nil {
		return f.errAdvance
	}
	f.pos.Seq, f.pos.NextRS = seq, nextRS
	return nil
}

func (f *fakeJournal) ResetRelayPublisher(context.Context, int64, uint64) error { return f.errReset }

func (f *fakeJournal) RepublishRelayFrom(_ context.Context, floorSeq int64) error {
	if f.errRepublish != nil {
		return f.errRepublish
	}
	f.pos.Seq = floorSeq
	return nil
}

func (f *fakeJournal) ReadRelayJournalSince(_ context.Context, seq int64, limit int) ([]sqlite.RelayJournalEvent, error) {
	if f.errRead != nil {
		return nil, f.errRead
	}
	var out []sqlite.RelayJournalEvent
	for _, r := range f.rows {
		if r.Seq > seq && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

// LookupRelayJournalSeqs answers from the fake's own rows unless a test
// pins an explicit map — so an ordinary fake journal agrees with itself
// and the tail-verify passes, exactly as a healthy store would.
func (f *fakeJournal) LookupRelayJournalSeqs(context.Context, []string) (map[string]int64, error) {
	if f.errLookup != nil {
		return nil, f.errLookup
	}
	if f.seqs != nil {
		return f.seqs, nil
	}
	out := make(map[string]int64, len(f.rows))
	for _, r := range f.rows {
		out[r.Event.EventID] = r.Seq
	}
	return out, nil
}

func fakeRow(seq int64, id string) sqlite.RelayJournalEvent {
	return sqlite.RelayJournalEvent{
		Seq: seq,
		Event: model.SyncEvent{
			EventID: id, ProjectPrefix: "PROJ", NodeID: "PROJ-1",
			OpType: model.OpCreateNode, Payload: json.RawMessage(`{}`),
			WallClockTS: 1, LamportClock: seq, VectorClock: model.VectorClock{},
			AuthorID: "a", AuthorMachineHash: "0123456789abcdef",
		},
	}
}

func fakeConfig(t *testing.T, j publisher.Journal) publisher.Config {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "segments")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return publisher.Config{
		Journal: j, SegmentsDir: dir, PeerID: testPeerID,
		Key: testKey, KeyEpoch: 1, Logger: slog.Default(),
	}
}

// TestPublishPending_StoreFailuresAreReportedNotSwallowed walks every
// way the journal side can fail. None of them may look like a
// divergence — that verdict summons an operator, and a database hiccup
// must not.
func TestPublishPending_StoreFailuresAreReportedNotSwallowed(t *testing.T) {
	boom := errors.New("database is unavailable")
	tests := []struct {
		name string
		fake *fakeJournal
	}{
		{"reading the cursor", &fakeJournal{errCursor: boom}},
		{"reading the journal", &fakeJournal{pos: sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1}, errRead: boom}},
		{"advancing the cursor", &fakeJournal{
			pos:        sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1},
			rows:       []sqlite.RelayJournalEvent{fakeRow(1, "e1")},
			errAdvance: boom,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := publisher.New(fakeConfig(t, tt.fake))
			require.NoError(t, err)
			_, err = p.PublishPending(context.Background())
			require.ErrorIs(t, err, boom)
			require.NotErrorIs(t, err, publisher.ErrPublisherDiverged)
			require.False(t, p.Diverged())
		})
	}
}

// TestVerifyTail_LookupFailureIsNotADivergence keeps a failed check
// distinct from a failed comparison: not knowing is not the same as
// knowing the journal diverged.
func TestVerifyTail_LookupFailureIsNotADivergence(t *testing.T) {
	boom := errors.New("database is unavailable")
	r := newRig(t)
	r.journalEvents(t, 2)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	cfg := r.config()
	cfg.Journal = &fakeJournal{
		pos:       sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 3},
		errLookup: boom,
	}
	p2, err := publisher.New(cfg)
	require.NoError(t, err)
	_, err = p2.PublishPending(ctx)
	require.ErrorIs(t, err, boom)
	require.False(t, p2.Diverged())
}

// TestVerifyTail_RefusesReorderedHistory covers the second divergence
// signal: the published events are all still present, but the journal
// now holds them in a different order — the same history cannot have
// produced both.
func TestVerifyTail_RefusesReorderedHistory(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 3)
	ctx := context.Background()

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	ids := r.journalIDs(t)
	reordered := map[string]int64{}
	for i, id := range ids {
		reordered[id] = int64(len(ids) - i) // strictly decreasing
	}

	cfg := r.config()
	cfg.Journal = &fakeJournal{
		pos:  sqlite.RelayPushPosition{PubEpoch: 1, NextRS: uint64(len(ids) + 1)},
		seqs: reordered,
	}
	p2, err := publisher.New(cfg)
	require.NoError(t, err)
	_, err = p2.PublishPending(ctx)
	require.ErrorIs(t, err, publisher.ErrPublisherDiverged)
	require.Contains(t, err.Error(), "out of order")
	require.Contains(t, err.Error(), "reset-peer")
}

// TestResetPeer_ReportsAStoreFailure keeps the recovery honest: if the
// reset did not persist, the publisher stays refused.
func TestResetPeer_ReportsAStoreFailure(t *testing.T) {
	boom := errors.New("database is unavailable")
	fake := &fakeJournal{pos: sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1}, errReset: boom}
	p, err := publisher.New(fakeConfig(t, fake))
	require.NoError(t, err)
	require.ErrorIs(t, p.ResetPeer(context.Background(), 0, 1), boom)
}

// bankRig publishes two records, then makes the segment directory
// read-only and sizes the next publisher so exactly one more record
// fits. The following record must rotate — and rotation needs to create
// a file the directory no longer permits — so an append fails partway
// through a batch, which is the state the banking path exists for.
//
// The directory is used rather than a planted file because any file
// named like a segment becomes the newest segment, and the writer would
// then refuse at startup instead of mid-batch.
func bankRig(t *testing.T) (*fakeJournal, publisher.Config, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "segments")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	fake := &fakeJournal{
		pos:  sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1},
		rows: []sqlite.RelayJournalEvent{fakeRow(1, "e1"), fakeRow(2, "e2")},
	}
	base := publisher.Config{
		Journal: fake, SegmentsDir: dir, PeerID: testPeerID, Key: testKey,
		KeyEpoch: 1, Logger: slog.Default(),
	}
	p, err := publisher.New(base)
	require.NoError(t, err)
	n, err := p.PublishPending(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, n)

	info, err := os.Stat(filepath.Join(dir, segment.FileName(1)))
	require.NoError(t, err)

	payload, err := json.Marshal(fakeRow(3, "e3").Event)
	require.NoError(t, err)
	frame := int64(segment.RecordHeaderSize + len(payload))

	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	fake.rows = append(fake.rows, fakeRow(3, "e3"), fakeRow(4, "e4"))
	next := base
	next.MaxSegmentBytes = info.Size() + frame
	return fake, next, dir
}

// TestPublishPending_BanksWhatLandedBeforeAFailure covers a partial
// batch. Records that reached the medium are real, so the cursor moves
// over exactly those — leaving it behind a growing prefix would make
// every retry longer than the last.
func TestPublishPending_BanksWhatLandedBeforeAFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	fake, cfg, _ := bankRig(t)

	p, err := publisher.New(cfg)
	require.NoError(t, err)
	n, err := p.PublishPending(context.Background())
	require.Error(t, err, "the rotation the fourth record needs must fail")
	require.Equal(t, 1, n, "the third record landed before the rotation was blocked")
	require.Equal(t, int64(3), fake.pos.Seq, "the cursor banks exactly what landed")
}

// TestPublishPending_BankFailureReportsBothCauses covers a partial batch
// whose cursor advance also fails: the caller learns about the append
// failure and the bookkeeping failure rather than only the second.
func TestPublishPending_BankFailureReportsBothCauses(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	boom := errors.New("database is unavailable")
	fake, cfg, _ := bankRig(t)
	fake.errAdvance = boom

	p, err := publisher.New(cfg)
	require.NoError(t, err)
	_, err = p.PublishPending(context.Background())
	require.Error(t, err)
	require.ErrorIs(t, err, boom, "the bookkeeping failure must survive alongside the append failure")
}

// TestVerifyTail_RefusesARecordWithoutAnEventID covers a payload the
// publisher cannot identify. It is not a divergence — the medium is
// carrying something this publisher did not write — so the check
// reports it rather than summoning the restore recovery.
func TestVerifyTail_RefusesARecordWithoutAnEventID(t *testing.T) {
	r := newRig(t)
	h := segment.Header{
		FormatVersion: segment.FormatVersion, Flags: segment.FlagAuthenticated,
		PeerID: testPeerID, SegmentNo: 1, FirstRS: 1, KeyEpoch: 1, PubEpoch: 1,
	}
	raw, err := h.MarshalBinary()
	require.NoError(t, err)
	raw, err = segment.AppendRecord(raw, h, 1, []byte(`{"no_event_id":true}`), testKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(r.segDir, segment.FileName(1)), raw, 0o600))

	r.journalEvents(t, 1)
	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(context.Background())
	require.Error(t, err)
	require.NotErrorIs(t, err, publisher.ErrPublisherDiverged)
	require.Contains(t, err.Error(), "event id")
}

// TestOnCommit_ReentrantCallReturnsImmediately pins the guard itself:
// a pass already in flight is skipped, because the tick is the
// guarantee and this trigger is only the fast path.
func TestOnCommit_ReentrantCallReturnsImmediately(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 2)

	p, err := publisher.New(r.config())
	require.NoError(t, err)

	handler := p.OnCommit()
	var reentered bool
	r.store.AddOnCommit(func() {
		if !reentered {
			reentered = true
			handler() // fires while the outer pass is still in flight
		}
	})
	require.NotPanics(t, handler)
	require.Equal(t, r.journalIDs(t), r.publishedEvents(t))
}

// TestCodeOf covers the verdict mapping.
func TestCodeOf(t *testing.T) {
	require.Equal(t, "", publisher.CodeOf(nil))
	require.Equal(t, "RELAY_PUBLISHER_DIVERGED", publisher.CodeOf(publisher.ErrPublisherDiverged))
	require.Equal(t, "RELAY_PUBLISHER_DIVERGED",
		publisher.CodeOf(fmt.Errorf("wrapped: %w", publisher.ErrPublisherDiverged)))
	require.Equal(t, "", publisher.CodeOf(errors.New("something else")))
	require.Equal(t, "", publisher.CodeOf(segment.ErrSegmentCorrupt))
}

// TestNew_DefaultsALogger keeps a caller that supplies none from
// panicking on the first warning.
func TestNew_DefaultsALogger(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 1)
	cfg := r.config()
	cfg.Logger = nil

	p, err := publisher.New(cfg)
	require.NoError(t, err)
	_, err = p.PublishPending(context.Background())
	require.NoError(t, err)
}

// TestPublishPending_NothingLandedReportsOnlyTheAppendFailure covers the
// batch that fails on its very first record: there is nothing to bank,
// so the cursor must not move at all.
func TestPublishPending_NothingLandedReportsOnlyTheAppendFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	dir := filepath.Join(t.TempDir(), "segments")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	fake := &fakeJournal{
		pos:  sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1},
		rows: []sqlite.RelayJournalEvent{fakeRow(1, "e1")},
	}
	base := publisher.Config{
		Journal: fake, SegmentsDir: dir, PeerID: testPeerID, Key: testKey,
		KeyEpoch: 1, Logger: slog.Default(),
	}
	p, err := publisher.New(base)
	require.NoError(t, err)
	_, err = p.PublishPending(context.Background())
	require.NoError(t, err)
	banked := fake.pos.Seq

	info, err := os.Stat(filepath.Join(dir, segment.FileName(1)))
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	fake.rows = append(fake.rows, fakeRow(2, "e2"))
	next := base
	next.MaxSegmentBytes = info.Size() // not even one more record fits

	p2, err := publisher.New(next)
	require.NoError(t, err)
	n, err := p2.PublishPending(context.Background())
	require.Error(t, err)
	require.Zero(t, n)
	require.Equal(t, banked, fake.pos.Seq, "nothing landed, so the cursor does not move")
}

// TestVerifyTail_RefusesASymlinkedSegment applies the FR-21 §5.1
// refusal to the publisher's own directory, on the startup path.
func TestVerifyTail_RefusesASymlinkedSegment(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 1)
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(r.segDir, segment.FileName(1))))

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(context.Background())
	require.ErrorIs(t, err, segment.ErrSymlink)
	require.False(t, p.Diverged(), "a planted link is not a divergence")
}

// TestVerifyTail_RefusesAnUnreadableTail covers the newest segment being
// unscannable — reported rather than treated as a restore.
func TestVerifyTail_RefusesAnUnreadableTail(t *testing.T) {
	r := newRig(t)
	r.journalEvents(t, 1)
	require.NoError(t, os.WriteFile(filepath.Join(r.segDir, segment.FileName(1)),
		[]byte("this is not a segment header at all, but it is long enough to be one -----------"), 0o600))

	p, err := publisher.New(r.config())
	require.NoError(t, err)
	_, err = p.PublishPending(context.Background())
	require.Error(t, err)
	require.NotErrorIs(t, err, publisher.ErrPublisherDiverged)
}
