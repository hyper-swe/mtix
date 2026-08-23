// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/stretchr/testify/require"
)

const (
	remotePeer = "fedcba9876543210"
	selfPeer   = "0123456789abcdef"
)

var fleetKey = []byte("fr21-ingest-fixed-test-key-32byt")

// peerRig is a relay directory holding one remote peer's stream, plus the
// local store that ingests it.
type peerRig struct {
	store    *sqlite.Store
	peersDir string
	segDir   string
	rs       uint64
	segNo    uint64
}

func newPeerRig(t *testing.T) *peerRig {
	t.Helper()
	root := t.TempDir()
	st, err := sqlite.New(filepath.Join(root, "store.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	peersDir := filepath.Join(root, "relay", "peers")
	segDir := filepath.Join(peersDir, remotePeer, "segments")
	require.NoError(t, os.MkdirAll(segDir, 0o700))
	return &peerRig{store: st, peersDir: peersDir, segDir: segDir, rs: 1, segNo: 1}
}

func (r *peerRig) config() ingest.Config {
	return ingest.Config{
		Store:      r.store,
		PeersDir:   r.peersDir,
		SelfPeerID: selfPeer,
		Keys:       fixedKey(fleetKey),
		Logger:     slog.Default(),
	}
}

// fixedKey is a KeySelector that answers with one key for every epoch —
// the key lifecycle itself is proved elsewhere.
type fixedKey []byte

func (k fixedKey) For(uint16) ([]byte, error) { return k, nil }

// event builds a valid sync event the far peer could have published.
func event(t *testing.T, op model.OpType, nodeID string, lamport int64, payload any) model.SyncEvent {
	t.Helper()
	pl, err := model.EncodePayload(payload)
	require.NoError(t, err)
	return model.SyncEvent{
		EventID:           clock.MustNewEventID(),
		ProjectPrefix:     "PROJ",
		NodeID:            nodeID,
		OpType:            op,
		Payload:           pl,
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      lamport,
		VectorClock:       model.VectorClock{"remote": lamport},
		AuthorID:          "remote",
		AuthorMachineHash: remotePeer,
	}
}

// publish writes a segment carrying the given payloads, authenticated
// unless key is nil.
func (r *peerRig) publish(t *testing.T, key []byte, payloads ...[]byte) {
	t.Helper()
	h := segment.Header{
		FormatVersion: segment.FormatVersion,
		PeerID:        remotePeer,
		SegmentNo:     r.segNo,
		FirstRS:       r.rs,
		KeyEpoch:      1,
		PubEpoch:      1,
	}
	if key != nil {
		h.Flags |= segment.FlagAuthenticated
	}
	raw, err := h.MarshalBinary()
	require.NoError(t, err)
	for _, p := range payloads {
		raw, err = segment.AppendRecord(raw, h, r.rs, p, key)
		require.NoError(t, err)
		r.rs++
	}
	require.NoError(t, os.WriteFile(filepath.Join(r.segDir, segment.FileName(r.segNo)), raw, 0o600))
	r.segNo++
}

func marshalEvents(t *testing.T, events ...model.SyncEvent) [][]byte {
	t.Helper()
	out := make([][]byte, 0, len(events))
	for _, e := range events {
		b, err := json.Marshal(e)
		require.NoError(t, err)
		out = append(out, b)
	}
	return out
}

func (r *peerRig) cursor(t *testing.T) sqlite.RelayIngestPosition {
	t.Helper()
	pos, err := r.store.RelayIngestCursor(context.Background(), remotePeer)
	require.NoError(t, err)
	return pos
}

func (r *peerRig) journalCount(t *testing.T) int {
	t.Helper()
	rows, err := r.store.ReadRelayJournalSince(context.Background(), 0, 1000)
	require.NoError(t, err)
	return len(rows)
}

// TestRelayIngest_AppliesRecordsThroughTheStandardPath is the base case:
// relay records become store state through the same apply path a hub
// pull uses, so the derived-state recomputes come along for free rather
// than being reimplemented here.
func TestRelayIngest_AppliesRecordsThroughTheStandardPath(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()

	e1 := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "from the far peer"})
	e2 := event(t, model.OpCreateNode, "PROJ-2", 2, &model.CreateNodePayload{Title: "second"})
	r.publish(t, fleetKey, marshalEvents(t, e1, e2)...)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, stats.Applied)
	require.Zero(t, stats.Quarantined)

	node, err := r.store.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Equal(t, "from the far peer", node.Title)

	// The events are journal rows now, which is what makes hook dispatch
	// origin-independent.
	require.Equal(t, 2, r.journalCount(t))

	pos := r.cursor(t)
	require.Equal(t, uint64(1), pos.SegmentNo)
	require.Equal(t, uint64(2), pos.RS)

	t.Run("a second poll with nothing new is a no-op", func(t *testing.T) {
		again, err := in.IngestAll(ctx)
		require.NoError(t, err)
		require.Zero(t, again.Applied)
		require.Equal(t, 2, r.journalCount(t))
	})
}

// TestRelayIngest_SkipsItsOwnDirectory keeps a peer from ingesting what
// it just published, which would be a loop with extra steps.
func TestRelayIngest_SkipsItsOwnDirectory(t *testing.T) {
	r := newPeerRig(t)
	own := filepath.Join(r.peersDir, selfPeer, "segments")
	require.NoError(t, os.MkdirAll(own, 0o700))

	e := event(t, model.OpCreateNode, "PROJ-9", 1, &model.CreateNodePayload{Title: "mine"})
	h := segment.Header{
		FormatVersion: segment.FormatVersion, Flags: segment.FlagAuthenticated,
		PeerID: selfPeer, SegmentNo: 1, FirstRS: 1, KeyEpoch: 1, PubEpoch: 1,
	}
	raw, err := h.MarshalBinary()
	require.NoError(t, err)
	raw, err = segment.AppendRecord(raw, h, 1, marshalEvents(t, e)[0], fleetKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(own, segment.FileName(1)), raw, 0o600))

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Zero(t, stats.Applied)
	require.Zero(t, r.journalCount(t))
}

// TestRelayIngest_HooksFireOnRelayArrivalViaTheLedger is the reason this
// deliverable exists, and the epic's headline claim: an event that
// arrived over a folder drives the same hooks as one that arrived over
// the hub, with ZERO changes to the dispatch path.
//
// It is proved through the real FR-20 ledger and a real hooks.yaml —
// not a mock — because the claim is precisely that nothing about
// dispatch had to learn the relay exists.
func TestRelayIngest_HooksFireOnRelayArrivalViaTheLedger(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()
	hooksDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "hooks.yaml"), []byte(`
hooks:
  - name: wake-worker
    match:
      events: [status.changed]
      status-to: [done]
      to-agent: worker
    deliver: [inbox]
`), 0o600))

	// This peer is already running: its journal holds local history, so
	// the poll below is ordinary traffic rather than a first attach.
	// (The bootstrap case — and why it must NOT wake anyone — is the
	// next test.)
	seed := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "relayed work"})
	require.NoError(t, r.store.WithTx(ctx, func(tx *sqlTx) error {
		return sqlite.IdempotentApply(ctx, tx, &seed)
	}))

	// The far peer drives it to done. That event arrives ONLY over the
	// relay — no hub, no network.
	done := event(t, model.OpTransitionStatus, "PROJ-1", 2, &model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusDone})
	r.publish(t, fleetKey, marshalEvents(t, done)...)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Applied)
	require.False(t, stats.BootstrapFloorInitialized, "a running peer's poll is not a bootstrap")

	// The dispatcher is the shipped one, reading the shipped journal.
	disp := service.NewHooksDispatcher(r.store, hooksDir, slog.Default())
	disp.Dispatch(ctx)

	entries, err := r.store.ReadHookLog(ctx, 100)
	require.NoError(t, err)
	delivered := 0
	for _, e := range entries {
		if e.Hook == "wake-worker" && e.Outcome == "delivered" {
			delivered++
		}
	}
	require.Equal(t, 1, delivered,
		"a relay-arrived status change must wake the worker exactly as a hub-arrived one would")

	t.Run("and it fires exactly once across restarts", func(t *testing.T) {
		service.NewHooksDispatcher(r.store, hooksDir, slog.Default()).Dispatch(ctx)
		entries, err := r.store.ReadHookLog(ctx, 100)
		require.NoError(t, err)
		n := 0
		for _, e := range entries {
			if e.Hook == "wake-worker" && e.Outcome == "delivered" {
				n++
			}
		}
		require.Equal(t, 1, n, "the ledger makes dispatch exactly-once per host")
	})
}

// TestRelayIngest_BootstrapFloorPreventsWakeStorm is the FR-20 §8
// bootstrap rule applied to a first attach: a store filling from empty
// is importing history, not receiving fresh work. Firing a hook per
// imported event would wake every agent in the fleet at once, which is
// the failure mode this rule exists to prevent.
func TestRelayIngest_BootstrapFloorPreventsWakeStorm(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()
	hooksDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "hooks.yaml"), []byte(`
hooks:
  - name: wake-worker
    match:
      events: [status.changed]
      status-to: [done]
      to-agent: worker
    deliver: [inbox]
`), 0o600))

	// A backlog of history arrives on a store that has never held an event.
	var payloads [][]byte
	payloads = append(payloads, marshalEvents(t, event(t, model.OpCreateNode, "PROJ-1", 1,
		&model.CreateNodePayload{Title: "history"}))...)
	for i := 0; i < 3; i++ {
		payloads = append(payloads, marshalEvents(t, event(t, model.OpTransitionStatus, "PROJ-1", int64(i+2),
			&model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusDone}))...)
	}
	r.publish(t, fleetKey, payloads...)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)
	require.Equal(t, len(payloads), stats.Applied)
	require.True(t, stats.BootstrapFloorInitialized, "a fill from empty is a bootstrap")

	service.NewHooksDispatcher(r.store, hooksDir, slog.Default()).Dispatch(ctx)
	entries, err := r.store.ReadHookLog(ctx, 100)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotEqual(t, "delivered", e.Outcome,
			"imported history must not fire a wake storm on first attach")
	}

	t.Run("but work arriving after the bootstrap does wake", func(t *testing.T) {
		fresh := event(t, model.OpTransitionStatus, "PROJ-1", 99,
			&model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusDone})
		r.publish(t, fleetKey, marshalEvents(t, fresh)...)

		stats, err := in.IngestAll(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, stats.Applied)
		require.False(t, stats.BootstrapFloorInitialized, "the floor is initialized once, not every poll")

		service.NewHooksDispatcher(r.store, hooksDir, slog.Default()).Dispatch(ctx)
		entries, err := r.store.ReadHookLog(ctx, 100)
		require.NoError(t, err)
		delivered := 0
		for _, e := range entries {
			if e.Outcome == "delivered" {
				delivered++
			}
		}
		require.Equal(t, 1, delivered, "post-bootstrap work wakes normally")
	})
}

// TestRelayIngest_HubAndRelayDoubleArrivalAppliesOnce covers a hybrid
// peer configured with both transports. Duplicate delivery is benign by
// construction — the same dedupe the hub path relies on — so a gateway
// peer needs no coordination between its two transports.
func TestRelayIngest_HubAndRelayDoubleArrivalAppliesOnce(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()

	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "arrives twice"})

	// The hub delivered it first, through the shipped apply path.
	require.NoError(t, r.store.WithTx(ctx, func(tx *sqlTx) error {
		return sqlite.IdempotentApply(ctx, tx, &e)
	}))
	require.Equal(t, 1, r.journalCount(t))

	// The same event then arrives over the relay.
	r.publish(t, fleetKey, marshalEvents(t, e)...)
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)

	require.Equal(t, 1, r.journalCount(t), "the second arrival must not double the journal")
	require.Equal(t, 1, stats.Applied, "the record was consumed, and applying it was a no-op")

	// And the cursor still advanced, so the relay does not re-read it.
	require.Equal(t, uint64(1), r.cursor(t).RS)
}

// TestRelayIngest_QuarantineSkipsAuthenticatedGarbageLoudly is the
// FR-21 §6.4 asymmetry, authenticated half. A record whose MAC verified
// came from a peer holding the fleet key, so a semantic failure is that
// peer sending garbage rather than an attack. Stalling the whole fleet
// on one malformed event from a trusted peer is worse than skipping it,
// so it is skipped — but loudly: logged, counted, and visible to
// doctor. Silence is what would make this dangerous.
func TestRelayIngest_QuarantineSkipsAuthenticatedGarbageLoudly(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()

	good1 := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "before"})
	good2 := event(t, model.OpCreateNode, "PROJ-2", 3, &model.CreateNodePayload{Title: "after"})

	// Semantically invalid, but correctly framed and MAC'd: the author
	// id breaks the §5.1 grammar.
	bad := event(t, model.OpCreateNode, "PROJ-BAD", 2, &model.CreateNodePayload{Title: "garbage"})
	bad.AuthorID = "NOT A VALID AUTHOR"

	r.publish(t, fleetKey, marshalEvents(t, good1, bad, good2)...)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err, "one bad record must not fail the poll")

	require.Equal(t, 2, stats.Applied, "the records around it still apply")
	require.Equal(t, 1, stats.Quarantined, "the bad one is counted, not swallowed")
	require.Len(t, stats.Quarantines, 1)
	require.Equal(t, remotePeer, stats.Quarantines[0].PeerID)
	require.Equal(t, uint64(2), stats.Quarantines[0].RS)
	require.NotEmpty(t, stats.Quarantines[0].Reason)

	// The stream is not stalled: the cursor moved past the whole segment.
	require.Equal(t, uint64(3), r.cursor(t).RS)

	_, err = r.store.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	_, err = r.store.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	_, err = r.store.GetNode(ctx, "PROJ-BAD")
	require.Error(t, err, "the quarantined record must not have been applied")
}

// TestRelayIngest_UnauthenticatedGarbageStallsHard is the other half of
// the §6.4 asymmetry, and the reason it is an asymmetry at all. On an
// unauthenticated relay there is no way to tell a trusted peer's bug
// from an attacker's crafted record, so the same failure is a hard
// stall rather than a skip: skipping would let anyone who can write the
// folder erase an event from the fleet's history by making it
// unparseable.
func TestRelayIngest_UnauthenticatedGarbageStallsHard(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()

	good := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "before"})
	bad := event(t, model.OpCreateNode, "PROJ-BAD", 2, &model.CreateNodePayload{Title: "garbage"})
	bad.AuthorID = "NOT A VALID AUTHOR"
	after := event(t, model.OpCreateNode, "PROJ-3", 3, &model.CreateNodePayload{Title: "after"})

	r.publish(t, nil, marshalEvents(t, good, bad, after)...)

	cfg := r.config()
	cfg.Keys = nil
	cfg.Unauthenticated = true
	in, err := ingest.New(cfg)
	require.NoError(t, err)

	stats, err := in.IngestAll(ctx)
	require.NoError(t, err, "a stall is a reported condition, not a crash")
	require.Equal(t, 1, stats.Applied, "records before the garbage still apply")
	require.Zero(t, stats.Quarantined, "an unauthenticated relay never quarantines")
	require.Len(t, stats.Stalls, 1)
	require.Equal(t, remotePeer, stats.Stalls[0].PeerID)

	// The cursor stops at the last good record, so the stall is durable
	// and the operator sees it until it is repaired.
	require.Equal(t, uint64(1), r.cursor(t).RS)
	_, err = r.store.GetNode(ctx, "PROJ-3")
	require.Error(t, err, "nothing past the stall may be applied")

	t.Run("and it stays stalled on the next poll", func(t *testing.T) {
		again, err := in.IngestAll(ctx)
		require.NoError(t, err)
		require.Len(t, again.Stalls, 1)
		require.Equal(t, uint64(1), r.cursor(t).RS)
	})
}

// TestRelayIngest_CursorNeverAdvancesPastACondemnedSegment is the rule
// carried over from the corpus work, now driven end to end through a
// real store.
//
// A sealed segment DELIVERS its clean prefix and is THEN condemned. The
// prefix is authentic and is applied — withholding it would strand the
// fleet — but the cursor must not move, because moving it would step
// over whatever the damage concealed.
func TestRelayIngest_CursorNeverAdvancesPastACondemnedSegment(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()

	// Segment 1: two good records, then damage, then a successor so
	// segment 1 is sealed.
	e1 := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "one"})
	e2 := event(t, model.OpCreateNode, "PROJ-2", 2, &model.CreateNodePayload{Title: "two"})
	e3 := event(t, model.OpCreateNode, "PROJ-3", 3, &model.CreateNodePayload{Title: "three"})
	r.publish(t, fleetKey, marshalEvents(t, e1, e2, e3)...)

	// Flip a bit in the third record's MAC: the first two still verify.
	path := filepath.Join(r.segDir, segment.FileName(1))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	twoOnly, err := segmentBytesFor(t, r, e1, e2)
	require.NoError(t, err)
	raw[len(twoOnly)+20] ^= 0x01
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	// A successor seals segment 1.
	e4 := event(t, model.OpCreateNode, "PROJ-4", 4, &model.CreateNodePayload{Title: "four"})
	r.publish(t, fleetKey, marshalEvents(t, e4)...)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)

	require.Equal(t, 2, stats.Applied, "the clean prefix is delivered, not withheld")
	require.Len(t, stats.Stalls, 1)
	require.Equal(t, "RELAY_SEGMENT_CORRUPT", stats.Stalls[0].Code)

	pos := r.cursor(t)
	require.Zero(t, pos.RS,
		"the cursor must not advance past a condemned segment even though records arrived")

	// And the successor's records are NEVER reached — stepping over the
	// damage is exactly what the stall prevents.
	_, err = r.store.GetNode(ctx, "PROJ-4")
	require.Error(t, err, "a reader must not skip a condemned segment to reach its successor")

	t.Run("repeated polls re-deliver the prefix and stay stalled", func(t *testing.T) {
		again, err := in.IngestAll(ctx)
		require.NoError(t, err)
		require.Equal(t, 2, again.Applied, "re-applying is idempotent")
		require.Equal(t, 2, r.journalCount(t), "and never duplicates the journal")
		require.Zero(t, r.cursor(t).RS)
	})
}

// segmentBytesFor rebuilds the prefix bytes for the given events so a
// test can locate a record boundary without hardcoding an offset.
func segmentBytesFor(t *testing.T, r *peerRig, events ...model.SyncEvent) ([]byte, error) {
	t.Helper()
	h := segment.Header{
		FormatVersion: segment.FormatVersion, Flags: segment.FlagAuthenticated,
		PeerID: remotePeer, SegmentNo: 1, FirstRS: 1, KeyEpoch: 1, PubEpoch: 1,
	}
	raw, err := h.MarshalBinary()
	if err != nil {
		return nil, err
	}
	for i, e := range events {
		b, mErr := json.Marshal(e)
		if mErr != nil {
			return nil, mErr
		}
		raw, err = segment.AppendRecord(raw, h, uint64(i+1), b, fleetKey)
		if err != nil {
			return nil, err
		}
	}
	return raw, nil
}

// TestRelayIngest_MACFailureCountsAuthFail is the FR-21 §8.2 signal: a
// spike in authentication failures for a peer means something that is
// not that peer is writing its directory.
func TestRelayIngest_MACFailureCountsAuthFail(t *testing.T) {
	r := newPeerRig(t)
	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "forged"})
	r.publish(t, fleetKey, marshalEvents(t, e)...)

	// Someone re-MAC'd the record with a key that is not the fleet's.
	path := filepath.Join(r.segDir, segment.FileName(1))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	raw[segment.HeaderSize+20] ^= 0x01
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	// A successor seals it. Until then the same bytes are indistinguishable
	// from an append in flight (§5.4), so a forged record at the ACTIVE tail
	// is deliberately not yet a signal — it becomes one when the segment can
	// no longer be growing.
	r.publish(t, fleetKey, marshalEvents(t,
		event(t, model.OpCreateNode, "PROJ-2", 2, &model.CreateNodePayload{Title: "later"}))...)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)

	require.Zero(t, stats.Applied)
	require.Equal(t, 1, stats.AuthFailures,
		"the §8.2 counter fires — a spike means something that is not this peer is writing its directory")
	require.Len(t, stats.Stalls, 1)
	require.Equal(t, "RELAY_SEGMENT_CORRUPT", stats.Stalls[0].Code,
		"the VERDICT is the frame's; RELAY_AUTH_FAIL is a counter, not a second verdict")
	require.Zero(t, r.cursor(t).RS, "an authentication failure stalls like any integrity verdict")
}

// TestRelayIngest_UIDConflictIsRejectedLoudly is the SYNC-DESIGN §14.6
// rule at arbiter-less ingest: the same uid arriving under a different
// display path is two different things wearing one identity, and
// guessing which is meant is exactly what the rule forbids.
func TestRelayIngest_UIDConflictIsRejectedLoudly(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()

	// A node exists locally with a known uid.
	local := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "local"})
	local.UID = clock.MustNewEventID()
	require.NoError(t, r.store.WithTx(ctx, func(tx *sqlTx) error {
		return sqlite.IdempotentApply(ctx, tx, &local)
	}))

	// The far peer sends the SAME uid under a different path.
	clash := event(t, model.OpUpdateField, "PROJ-99", 2, &model.UpdateFieldPayload{
		FieldName: "title", NewValue: json.RawMessage(`"elsewhere"`),
	})
	clash.UID = local.UID
	r.publish(t, fleetKey, marshalEvents(t, clash)...)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)

	require.Equal(t, 1, stats.Quarantined, "a uid conflict is loud, never guessed")
	require.Contains(t, stats.Quarantines[0].Reason, "uid")

	t.Run("but the identical uid at the identical path is an idempotent no-op", func(t *testing.T) {
		same := event(t, model.OpUpdateField, "PROJ-1", 3, &model.UpdateFieldPayload{
			FieldName: "title", NewValue: json.RawMessage(`"renamed"`),
		})
		same.UID = local.UID
		r.publish(t, fleetKey, marshalEvents(t, same)...)

		stats, err := in.IngestAll(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, stats.Applied)
		require.Zero(t, stats.Quarantined)

		node, err := r.store.GetNode(ctx, "PROJ-1")
		require.NoError(t, err)
		require.Equal(t, "renamed", node.Title)
	})
}

// TestRelayIngest_ConfigValidation refuses an unusable ingestor.
func TestRelayIngest_ConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ingest.Config)
	}{
		{"no store", func(c *ingest.Config) { c.Store = nil }},
		{"self peer id off grammar", func(c *ingest.Config) { c.SelfPeerID = "NOPE" }},
		{"authenticated without keys", func(c *ingest.Config) { c.Keys = nil }},
		{"keys with the unauthenticated mode", func(c *ingest.Config) { c.Unauthenticated = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newPeerRig(t)
			cfg := r.config()
			tt.mutate(&cfg)
			_, err := ingest.New(cfg)
			require.Error(t, err)
		})
	}
}

// TestRelayIngest_MissingPeersDirectory reports an unreachable medium as
// itself. FR-21 §6.2's contract is symmetric: a dead relay is degraded
// transport, and ingest must not dress it up as corruption.
func TestRelayIngest_MissingPeersDirectory(t *testing.T) {
	r := newPeerRig(t)
	cfg := r.config()
	cfg.PeersDir = filepath.Join(t.TempDir(), "gone")

	in, err := ingest.New(cfg)
	require.NoError(t, err)
	_, err = in.IngestAll(context.Background())
	require.Error(t, err)
	require.Empty(t, segment.CodeOf(err))
}

// TestRelayIngest_ForeignPeerEntriesAreReportedNotParsed keeps the §5.2
// grammar at the directory boundary.
func TestRelayIngest_ForeignPeerEntriesAreReportedNotParsed(t *testing.T) {
	r := newPeerRig(t)
	require.NoError(t, os.MkdirAll(filepath.Join(r.peersDir, "bootstrap"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(r.peersDir, "notes.txt"), []byte("x"), 0o600))

	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "ok"})
	r.publish(t, fleetKey, marshalEvents(t, e)...)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, stats.Applied)
	require.ElementsMatch(t, []string{"bootstrap", "notes.txt"}, stats.ForeignEntries)
}

// TestRelayIngest_ResumesMidStream covers a poll that starts inside a
// segment it has already partly applied.
func TestRelayIngest_ResumesMidStream(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()

	e1 := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "one"})
	r.publish(t, fleetKey, marshalEvents(t, e1)...)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	_, err = in.IngestAll(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), r.cursor(t).RS)

	// A second segment continues the stream.
	e2 := event(t, model.OpCreateNode, "PROJ-2", 2, &model.CreateNodePayload{Title: "two"})
	r.publish(t, fleetKey, marshalEvents(t, e2)...)

	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Applied, "only the new record is applied")
	require.Equal(t, uint64(2), r.cursor(t).RS)
	require.Equal(t, 2, r.journalCount(t))
}

// sqlTx aliases the transaction handle the store hands its callbacks, so
// the tests can call the shipped apply path directly.
type sqlTx = sql.Tx

// fakeStore drives the store-side failure paths. The narrow Store
// interface exists so these can be exercised without a broken database.
type fakeStore struct {
	inner        *sqlite.Store
	errCursor    error
	errAdvance   error
	errTail      error
	errFloor     error
	errResolveBy error
	errTx        error
}

func (f *fakeStore) RelayIngestCursor(ctx context.Context, peerID string) (sqlite.RelayIngestPosition, error) {
	if f.errCursor != nil {
		return sqlite.RelayIngestPosition{}, f.errCursor
	}
	return f.inner.RelayIngestCursor(ctx, peerID)
}

func (f *fakeStore) AdvanceRelayIngestCursor(ctx context.Context, peerID string, pos sqlite.RelayIngestPosition) error {
	if f.errAdvance != nil {
		return f.errAdvance
	}
	return f.inner.AdvanceRelayIngestCursor(ctx, peerID, pos)
}

func (f *fakeStore) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if f.errTx != nil {
		return f.errTx
	}
	return f.inner.WithTx(ctx, fn)
}

func (f *fakeStore) ResolveDisplayPathByUID(ctx context.Context, uid string) (string, error) {
	if f.errResolveBy != nil {
		return "", f.errResolveBy
	}
	return f.inner.ResolveDisplayPathByUID(ctx, uid)
}

func (f *fakeStore) JournalTail(ctx context.Context) (int64, error) {
	if f.errTail != nil {
		return 0, f.errTail
	}
	return f.inner.JournalTail(ctx)
}

func (f *fakeStore) InitHookScanFloorAtTail(ctx context.Context) error {
	if f.errFloor != nil {
		return f.errFloor
	}
	return f.inner.InitHookScanFloorAtTail(ctx)
}

// withFake returns a config whose store is a fake wrapping the real one.
func (r *peerRig) withFake(f *fakeStore) ingest.Config {
	f.inner = r.store
	cfg := r.config()
	cfg.Store = f
	return cfg
}

// TestRelayIngest_StoreFailuresAreReportedNotSwallowed walks the store
// side. None of these may look like a corruption verdict — that summons
// an operator to inspect a medium that is fine.
func TestRelayIngest_StoreFailuresAreReportedNotSwallowed(t *testing.T) {
	boom := errors.New("database is unavailable")
	tests := []struct {
		name string
		fake *fakeStore
	}{
		{"reading the ingest cursor", &fakeStore{errCursor: boom}},
		{"advancing the ingest cursor", &fakeStore{errAdvance: boom}},
		{"initializing the hook scan floor", &fakeStore{errFloor: boom}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newPeerRig(t)
			e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"})
			r.publish(t, fleetKey, marshalEvents(t, e)...)

			in, err := ingest.New(r.withFake(tt.fake))
			require.NoError(t, err)
			_, err = in.IngestAll(context.Background())
			require.ErrorIs(t, err, boom)
			require.Empty(t, segment.CodeOf(err), "a local database failure is not a medium verdict")
		})
	}
}

// TestRelayIngest_JournalTailFailureSkipsTheBootstrapDecision keeps a
// poll working when the bootstrap probe cannot run: better to ingest
// without moving the floor than to refuse the events entirely.
func TestRelayIngest_JournalTailFailureSkipsTheBootstrapDecision(t *testing.T) {
	r := newPeerRig(t)
	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"})
	r.publish(t, fleetKey, marshalEvents(t, e)...)

	in, err := ingest.New(r.withFake(&fakeStore{errTail: errors.New("unavailable")}))
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, stats.Applied)
	require.False(t, stats.BootstrapFloorInitialized)
}

// TestRelayIngest_ApplyFailureIsQuarantinedOnAnAuthenticatedRelay keeps
// the §6.4 policy consistent for a record the apply path itself rejects.
func TestRelayIngest_ApplyFailureIsQuarantinedOnAnAuthenticatedRelay(t *testing.T) {
	r := newPeerRig(t)
	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"})
	r.publish(t, fleetKey, marshalEvents(t, e)...)

	in, err := ingest.New(r.withFake(&fakeStore{errTx: errors.New("apply failed")}))
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, stats.Quarantined)
	require.Zero(t, stats.Applied)
}

// TestRelayIngest_UIDLookupFailureIsNotAConflict separates "cannot check"
// from "conflicts" — the second rejects a peer's work, the first is a
// local database problem.
func TestRelayIngest_UIDLookupFailureIsNotAConflict(t *testing.T) {
	r := newPeerRig(t)
	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"})
	e.UID = clock.MustNewEventID()
	r.publish(t, fleetKey, marshalEvents(t, e)...)

	in, err := ingest.New(r.withFake(&fakeStore{errResolveBy: errors.New("unavailable")}))
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, stats.Quarantined)
	require.Contains(t, stats.Quarantines[0].Reason, "unavailable")
}

// TestRelayIngest_MalformedPayloadIsQuarantined covers a record whose
// bytes are authentic but are not a sync event at all.
func TestRelayIngest_MalformedPayloadIsQuarantined(t *testing.T) {
	r := newPeerRig(t)
	r.publish(t, fleetKey, []byte("this is not an event"))

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, stats.Quarantined)
	require.Zero(t, stats.Applied)
}

// TestRelayIngest_MediumHazardsStall covers the ways a peer's directory
// can be unreadable. Each stops that peer's stream and none is confused
// with a semantic failure.
func TestRelayIngest_MediumHazardsStall(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, r *peerRig)
		wantErr error
	}{
		{"a symlinked segment", func(t *testing.T, r *peerRig) {
			require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(r.segDir, segment.FileName(1))))
		}, segment.ErrSymlink},
		{"bytes that are not a segment", func(t *testing.T, r *peerRig) {
			require.NoError(t, os.WriteFile(filepath.Join(r.segDir, segment.FileName(1)),
				[]byte("not a segment header, but long enough to look like one ------------------------"), 0o600))
		}, segment.ErrSegmentCorrupt},
		{"a segment claiming another peer", func(t *testing.T, r *peerRig) {
			h := segment.Header{
				FormatVersion: segment.FormatVersion, Flags: segment.FlagAuthenticated,
				PeerID: selfPeer, SegmentNo: 1, FirstRS: 1, KeyEpoch: 1, PubEpoch: 1,
			}
			raw, err := h.MarshalBinary()
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(r.segDir, segment.FileName(1)), raw, 0o600))
		}, segment.ErrPeerIDConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newPeerRig(t)
			tt.arrange(t, r)

			in, err := ingest.New(r.config())
			require.NoError(t, err)
			stats, err := in.IngestAll(context.Background())
			require.NoError(t, err, "one peer's medium problem must not fail the whole poll")
			require.Len(t, stats.Stalls, 1)
			require.Zero(t, stats.Applied)
			require.Zero(t, r.cursor(t).RS)
		})
	}
}

// TestRelayIngest_AuthenticatedSegmentOnAnUnauthenticatedRelay refuses a
// mode mismatch rather than resolving it — the §8.3 rule at the record
// level.
func TestRelayIngest_AuthenticatedSegmentOnAnUnauthenticatedRelay(t *testing.T) {
	r := newPeerRig(t)
	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"})
	r.publish(t, fleetKey, marshalEvents(t, e)...)

	cfg := r.config()
	cfg.Keys = nil
	cfg.Unauthenticated = true
	in, err := ingest.New(cfg)
	require.NoError(t, err)

	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Len(t, stats.Stalls, 1)
	require.Zero(t, stats.Applied)
}

// TestRelayIngest_PeerWithoutSegmentsIsSkipped covers a peer directory
// created by attach but not yet published to.
func TestRelayIngest_PeerWithoutSegmentsIsSkipped(t *testing.T) {
	r := newPeerRig(t)
	require.NoError(t, os.MkdirAll(filepath.Join(r.peersDir, "aaaaaaaaaaaaaaaa"), 0o700))

	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"})
	r.publish(t, fleetKey, marshalEvents(t, e)...)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, stats.Applied)
	require.Empty(t, stats.Stalls)
}

// TestRelayIngest_EmptySegmentDirectoryIsSkipped covers a peer whose
// segments directory exists but holds nothing yet.
func TestRelayIngest_EmptySegmentDirectoryIsSkipped(t *testing.T) {
	r := newPeerRig(t)
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Zero(t, stats.Applied)
	require.Empty(t, stats.Stalls)
}

// TestRelayIngest_KeySelectionFailureStalls covers an epoch the reader
// holds no key for — the D-R10 operator gap, which must not read as
// forgery.
func TestRelayIngest_KeySelectionFailureStalls(t *testing.T) {
	r := newPeerRig(t)
	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"})
	r.publish(t, fleetKey, marshalEvents(t, e)...)

	cfg := r.config()
	cfg.Keys = missingKey{}
	in, err := ingest.New(cfg)
	require.NoError(t, err)

	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Len(t, stats.Stalls, 1)
	require.Contains(t, stats.Stalls[0].Reason, "no key")
	require.Zero(t, stats.AuthFailures, "a missing key is an operator gap, never a forgery signal")
}

type missingKey struct{}

func (missingKey) For(uint16) ([]byte, error) { return nil, errors.New("no key for that epoch") }

// TestRelayIngest_NewDefaultsALogger keeps a caller that supplies none
// from panicking on the first stall.
func TestRelayIngest_NewDefaultsALogger(t *testing.T) {
	r := newPeerRig(t)
	cfg := r.config()
	cfg.Logger = nil
	in, err := ingest.New(cfg)
	require.NoError(t, err)
	_, err = in.IngestAll(context.Background())
	require.NoError(t, err)
}

// TestRelayIngest_UnreadableSegmentStalls covers a segment found by the
// walker that cannot then be opened.
func TestRelayIngest_UnreadableSegmentStalls(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	r := newPeerRig(t)
	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"})
	r.publish(t, fleetKey, marshalEvents(t, e)...)

	path := filepath.Join(r.segDir, segment.FileName(1))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Len(t, stats.Stalls, 1)
	require.Empty(t, stats.Stalls[0].Code, "an unreadable file is a medium problem, not a verdict")
	require.Zero(t, stats.Applied)
}
