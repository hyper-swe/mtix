// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// TestRelayPushCursor_StartsAtTheBeginning covers a store that has
// never published: the cursor sits below every journal row, and the
// publisher's epoch and relay sequence start at one.
func TestRelayPushCursor_StartsAtTheBeginning(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	pos, err := s.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), pos.Seq)
	require.Equal(t, uint16(1), pos.PubEpoch)
	require.Equal(t, uint64(1), pos.NextRS)
}

// TestAdvanceRelayPushCursor_IsMonotonic pins the FR-21 §6.2 ordering:
// the cursor advances only after the append returns, and never moves
// backwards on its own. A stale advance arriving out of order must not
// re-expose events that were already published.
func TestAdvanceRelayPushCursor_IsMonotonic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.AdvanceRelayPushCursor(ctx, 10, 5))
	pos, err := s.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(10), pos.Seq)
	require.Equal(t, uint64(5), pos.NextRS)

	// A lower advance is absorbed, not applied.
	require.NoError(t, s.AdvanceRelayPushCursor(ctx, 4, 2))
	pos, err = s.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(10), pos.Seq)
	require.Equal(t, uint64(5), pos.NextRS)

	require.NoError(t, s.AdvanceRelayPushCursor(ctx, 11, 6))
	pos, err = s.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(11), pos.Seq)
	require.Equal(t, uint64(6), pos.NextRS)
}

// TestResetRelayPublisher_BumpsEpochAndRewinds is the operator-gated
// half of FR-21 §5.7. reset-peer is the one path allowed to move the
// cursor backwards, and it may only do so while bumping the publisher
// epoch — post-restore events then carry a fresh (pub_epoch, rs) that
// no reader has consumed, which is precisely what stops the restored
// peer's new work being silently discarded.
func TestResetRelayPublisher_BumpsEpochAndRewinds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.AdvanceRelayPushCursor(ctx, 100, 40))

	require.NoError(t, s.ResetRelayPublisher(ctx, 20, 1))
	pos, err := s.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(20), pos.Seq, "rewound to the declared safe floor")
	require.Equal(t, uint16(2), pos.PubEpoch, "the epoch is bumped, never reused")
	require.Equal(t, uint64(1), pos.NextRS, "relay sequence restarts at the declared base")

	// And ordinary advances resume from there.
	require.NoError(t, s.AdvanceRelayPushCursor(ctx, 25, 6))
	pos, err = s.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(25), pos.Seq)
	require.Equal(t, uint16(2), pos.PubEpoch)
}

// TestResetRelayPublisher_EpochAlwaysMovesForward keeps two restores
// from colliding on one epoch, which would put post-restore events from
// different histories at the same (pub_epoch, rs).
func TestResetRelayPublisher_EpochAlwaysMovesForward(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.ResetRelayPublisher(ctx, 0, 1))
	require.NoError(t, s.ResetRelayPublisher(ctx, 0, 1))
	pos, err := s.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, uint16(3), pos.PubEpoch)
}

// TestResetRelayPublisher_RefusesAnInvalidBase keeps rs starting at a
// real position — zero means "absent" everywhere else in the format.
func TestResetRelayPublisher_RefusesAnInvalidBase(t *testing.T) {
	s := newTestStore(t)
	require.Error(t, s.ResetRelayPublisher(context.Background(), 0, 0))
}

// TestRelayIngestCursor_PerPeer covers the §6.3 per-peer read position.
func TestRelayIngestCursor_PerPeer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const peerA, peerB = "0123456789abcdef", "fedcba9876543210"

	got, err := s.RelayIngestCursor(ctx, peerA)
	require.NoError(t, err)
	require.Zero(t, got.SegmentNo)
	require.Zero(t, got.RS)

	require.NoError(t, s.AdvanceRelayIngestCursor(ctx, peerA, sqlite.RelayIngestPosition{
		SegmentNo: 2, RS: 51, PubEpoch: 1,
	}))
	require.NoError(t, s.AdvanceRelayIngestCursor(ctx, peerB, sqlite.RelayIngestPosition{
		SegmentNo: 9, RS: 3, PubEpoch: 4,
	}))

	got, err = s.RelayIngestCursor(ctx, peerA)
	require.NoError(t, err)
	require.Equal(t, sqlite.RelayIngestPosition{SegmentNo: 2, RS: 51, PubEpoch: 1}, got)

	got, err = s.RelayIngestCursor(ctx, peerB)
	require.NoError(t, err)
	require.Equal(t, sqlite.RelayIngestPosition{SegmentNo: 9, RS: 3, PubEpoch: 4}, got)
}

// TestAdvanceRelayIngestCursor_NeverGoesBackwardsWithinAnEpoch is the
// monotonic watermark FR-21 §8.2 relies on to refuse re-delivery below
// the reader's position. Within one publisher epoch a lower position is
// a replay, and absorbing it silently is the whole point.
func TestAdvanceRelayIngestCursor_NeverGoesBackwardsWithinAnEpoch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const peer = "0123456789abcdef"

	require.NoError(t, s.AdvanceRelayIngestCursor(ctx, peer, sqlite.RelayIngestPosition{
		SegmentNo: 3, RS: 51, PubEpoch: 1,
	}))
	require.NoError(t, s.AdvanceRelayIngestCursor(ctx, peer, sqlite.RelayIngestPosition{
		SegmentNo: 2, RS: 10, PubEpoch: 1,
	}))

	got, err := s.RelayIngestCursor(ctx, peer)
	require.NoError(t, err)
	require.Equal(t, sqlite.RelayIngestPosition{SegmentNo: 3, RS: 51, PubEpoch: 1}, got)
}

// TestAdvanceRelayIngestCursor_AcceptsAnEpochBump is the §5.7 reader
// side: a publisher epoch bump restarts relay sequences at a declared
// base, so a LOWER rs under a HIGHER epoch is legitimate forward
// progress rather than a replay. Refusing it here would reintroduce
// exactly the silent discard the epoch exists to prevent.
func TestAdvanceRelayIngestCursor_AcceptsAnEpochBump(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const peer = "0123456789abcdef"

	require.NoError(t, s.AdvanceRelayIngestCursor(ctx, peer, sqlite.RelayIngestPosition{
		SegmentNo: 7, RS: 900, PubEpoch: 1,
	}))
	require.NoError(t, s.AdvanceRelayIngestCursor(ctx, peer, sqlite.RelayIngestPosition{
		SegmentNo: 8, RS: 1, PubEpoch: 2,
	}))

	got, err := s.RelayIngestCursor(ctx, peer)
	require.NoError(t, err)
	require.Equal(t, sqlite.RelayIngestPosition{SegmentNo: 8, RS: 1, PubEpoch: 2}, got)
}

// TestAdvanceRelayIngestCursor_RefusesAnEpochRollback closes the
// rollback splice at the durable boundary: an epoch moving backwards is
// refused even though its rs would look like progress.
func TestAdvanceRelayIngestCursor_RefusesAnEpochRollback(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const peer = "0123456789abcdef"

	require.NoError(t, s.AdvanceRelayIngestCursor(ctx, peer, sqlite.RelayIngestPosition{
		SegmentNo: 8, RS: 1, PubEpoch: 2,
	}))
	require.NoError(t, s.AdvanceRelayIngestCursor(ctx, peer, sqlite.RelayIngestPosition{
		SegmentNo: 9, RS: 5000, PubEpoch: 1,
	}))

	got, err := s.RelayIngestCursor(ctx, peer)
	require.NoError(t, err)
	require.Equal(t, uint16(2), got.PubEpoch, "an older epoch never takes the cursor")
	require.Equal(t, uint64(1), got.RS)
}

// TestRelayIngestCursor_RejectsAnOffGrammarPeer keeps a malformed id
// out of the cursor table, where it would shadow a real peer's position.
func TestRelayIngestCursor_RejectsAnOffGrammarPeer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.Error(t, s.AdvanceRelayIngestCursor(ctx, "NOT A PEER", sqlite.RelayIngestPosition{
		SegmentNo: 1, RS: 1, PubEpoch: 1,
	}))
	_, err := s.RelayIngestCursor(ctx, "")
	require.Error(t, err)
}

// TestRelayCursors_SurviveReopen proves the tables are durable local
// meta rather than session state, and that the additive migration is
// idempotent across opens.
func TestRelayCursors_SurviveReopen(t *testing.T) {
	dir := t.TempDir() + "/relay-cursor.db"
	ctx := context.Background()

	first, err := sqlite.New(dir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, first.AdvanceRelayPushCursor(ctx, 42, 7))
	require.NoError(t, first.AdvanceRelayIngestCursor(ctx, "0123456789abcdef",
		sqlite.RelayIngestPosition{SegmentNo: 1, RS: 2, PubEpoch: 1}))
	require.NoError(t, first.Close())

	second, err := sqlite.New(dir, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	pos, err := second.RelayPushCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(42), pos.Seq)
	require.Equal(t, uint64(7), pos.NextRS)

	ingest, err := second.RelayIngestCursor(ctx, "0123456789abcdef")
	require.NoError(t, err)
	require.Equal(t, uint64(2), ingest.RS)
}

// TestRelayCursors_RefuseDamagedRows covers a local row that could not
// have come from a frame. Turning a negative position into a vast
// unsigned one would hand the publisher a cursor far past the journal
// and silently skip every event behind it, so the read refuses instead.
func TestRelayCursors_RefuseDamagedRows(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name  string
		setup string
		read  func(*sqlite.Store) error
	}{
		{
			name:  "negative publish sequence base",
			setup: `INSERT INTO relay_push_cursor (id, cursor, pub_epoch, next_rs) VALUES (1, 5, 1, -1)`,
			read:  func(s *sqlite.Store) error { _, err := s.RelayPushCursor(ctx); return err },
		},
		{
			name:  "publisher epoch past the frame's width",
			setup: `INSERT INTO relay_push_cursor (id, cursor, pub_epoch, next_rs) VALUES (1, 5, 70000, 1)`,
			read:  func(s *sqlite.Store) error { _, err := s.RelayPushCursor(ctx); return err },
		},
		{
			name:  "negative publisher epoch",
			setup: `INSERT INTO relay_push_cursor (id, cursor, pub_epoch, next_rs) VALUES (1, 5, -1, 1)`,
			read:  func(s *sqlite.Store) error { _, err := s.RelayPushCursor(ctx); return err },
		},
		{
			name: "negative ingest segment",
			setup: `INSERT INTO relay_ingest_cursor (peer_id, segment_no, rs, pub_epoch)
			        VALUES ('0123456789abcdef', -1, 1, 1)`,
			read: func(s *sqlite.Store) error {
				_, err := s.RelayIngestCursor(ctx, "0123456789abcdef")
				return err
			},
		},
		{
			name: "negative ingest relay sequence",
			setup: `INSERT INTO relay_ingest_cursor (peer_id, segment_no, rs, pub_epoch)
			        VALUES ('0123456789abcdef', 1, -1, 1)`,
			read: func(s *sqlite.Store) error {
				_, err := s.RelayIngestCursor(ctx, "0123456789abcdef")
				return err
			},
		},
		{
			name: "ingest epoch past the frame's width",
			setup: `INSERT INTO relay_ingest_cursor (peer_id, segment_no, rs, pub_epoch)
			        VALUES ('0123456789abcdef', 1, 1, 70000)`,
			read: func(s *sqlite.Store) error {
				_, err := s.RelayIngestCursor(ctx, "0123456789abcdef")
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "damaged.db")
			s, err := sqlite.New(dbPath, slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { _ = s.Close() })

			db := newTestDB(t, dbPath)
			_, err = db.Exec(tt.setup)
			require.NoError(t, err)

			require.Error(t, tt.read(s))
		})
	}
}

// TestReadRelayJournalSince_ReturnsWholeEventsInLocalOrder covers the
// publish read. It returns the event verbatim because the reader on the
// far side runs the same validation envelope a hub would, and can only
// do that with every field present.
func TestReadRelayJournalSince_ReturnsWholeEventsInLocalOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "first", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "second", now)))

	all, err := s.ReadRelayJournalSince(ctx, 0, 100)
	require.NoError(t, err)
	require.NotEmpty(t, all, "creating nodes must journal events")

	for i, rec := range all {
		require.NotEmpty(t, rec.Event.EventID, "row %d", i)
		require.NotEmpty(t, rec.Event.NodeID, "row %d", i)
		require.NotEmpty(t, rec.Event.OpType, "row %d", i)
		require.NotEmpty(t, rec.Event.Payload, "row %d", i)
		require.NotNil(t, rec.Event.VectorClock, "row %d", i)
		if i > 0 {
			require.Greater(t, rec.Seq, all[i-1].Seq, "rows must arrive in local insert order")
		}
	}

	// Reading past a cursor returns only what follows it.
	rest, err := s.ReadRelayJournalSince(ctx, all[0].Seq, 100)
	require.NoError(t, err)
	require.Len(t, rest, len(all)-1)
	require.Equal(t, all[1].Event.EventID, rest[0].Event.EventID)

	// And past the tail, nothing.
	none, err := s.ReadRelayJournalSince(ctx, all[len(all)-1].Seq, 100)
	require.NoError(t, err)
	require.Empty(t, none)
}

// TestReadRelayJournalSince_RespectsTheBatchLimit keeps a tick's read
// bounded, which is what makes publish cost track new events rather
// than history.
func TestReadRelayJournalSince_RespectsTheBatchLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("PROJ-%d", i+10)
		require.NoError(t, s.CreateNode(ctx, makeRootNode(id, "PROJ", "n", now)))
	}

	batch, err := s.ReadRelayJournalSince(ctx, 0, 2)
	require.NoError(t, err)
	require.Len(t, batch, 2)

	defaulted, err := s.ReadRelayJournalSince(ctx, 0, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(defaulted), 5, "a non-positive limit falls back to the default batch")
}

// TestRelayCursors_OnAClosedStore covers the medium-error paths. A relay
// cursor operation that cannot reach the database must report that
// plainly — FR-21 §6.2 requires a publish failure to be logged and
// retried, and the caller can only make that call from a real error.
func TestRelayCursors_OnAClosedStore(t *testing.T) {
	ctx := context.Background()
	newClosed := func(t *testing.T) *sqlite.Store {
		t.Helper()
		s, err := sqlite.New(filepath.Join(t.TempDir(), "closed.db"), slog.Default())
		require.NoError(t, err)
		require.NoError(t, s.Close())
		return s
	}

	tests := []struct {
		name string
		call func(*sqlite.Store) error
	}{
		{"read push cursor", func(s *sqlite.Store) error { _, err := s.RelayPushCursor(ctx); return err }},
		{"advance push cursor", func(s *sqlite.Store) error { return s.AdvanceRelayPushCursor(ctx, 1, 1) }},
		{"reset publisher", func(s *sqlite.Store) error { return s.ResetRelayPublisher(ctx, 0, 1) }},
		{"read ingest cursor", func(s *sqlite.Store) error {
			_, err := s.RelayIngestCursor(ctx, "0123456789abcdef")
			return err
		}},
		{"advance ingest cursor", func(s *sqlite.Store) error {
			return s.AdvanceRelayIngestCursor(ctx, "0123456789abcdef",
				sqlite.RelayIngestPosition{SegmentNo: 1, RS: 1, PubEpoch: 1})
		}},
		{"read journal", func(s *sqlite.Store) error {
			_, err := s.ReadRelayJournalSince(ctx, 0, 10)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, tt.call(newClosed(t)))
		})
	}
}

// TestResetRelayPublisher_RefusesANegativeFloor keeps the rewind inside
// the journal.
func TestResetRelayPublisher_RefusesANegativeFloor(t *testing.T) {
	s := newTestStore(t)
	require.Error(t, s.ResetRelayPublisher(context.Background(), -1, 1))
}

// TestReadRelayJournalSince_RefusesADamagedVectorClock stops a row that
// cannot be framed. A relay record carries the event verbatim, so an
// undecodable clock is not something to publish around.
func TestReadRelayJournalSince_RefusesADamagedVectorClock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "damaged-vc.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	db := newTestDB(t, dbPath)
	_, err = db.Exec(`
		INSERT INTO sync_events (event_id, project_prefix, node_id, op_type, payload,
		                         wall_clock_ts, lamport_clock, vector_clock,
		                         author_id, author_machine_hash, created_at)
		VALUES ('e1','PROJ','PROJ-1','create_node','{}',1,1,'not json','a','0123456789abcdef','now')`)
	require.NoError(t, err)

	_, err = s.ReadRelayJournalSince(context.Background(), 0, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "vector clock")
}

// TestLookupRelayJournalSeqs is the primitive the FR-21 §5.7 tail-verify
// asks its one question with: are the events this peer already published
// still in its journal, in the same order?
func TestLookupRelayJournalSeqs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "first", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "second", now)))

	all, err := s.ReadRelayJournalSince(ctx, 0, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 2)

	ids := []string{all[0].Event.EventID, all[1].Event.EventID, "never-existed"}
	got, err := s.LookupRelayJournalSeqs(ctx, ids)
	require.NoError(t, err)
	require.Len(t, got, 2, "an id the journal never held is simply absent")
	require.Equal(t, all[0].Seq, got[all[0].Event.EventID])
	require.Equal(t, all[1].Seq, got[all[1].Event.EventID])
	require.NotContains(t, got, "never-existed")

	t.Run("no ids is not a query", func(t *testing.T) {
		empty, err := s.LookupRelayJournalSeqs(ctx, nil)
		require.NoError(t, err)
		require.Empty(t, empty)
	})

	t.Run("a closed store reports the medium error", func(t *testing.T) {
		closed, err := sqlite.New(filepath.Join(t.TempDir(), "closed.db"), slog.Default())
		require.NoError(t, err)
		require.NoError(t, closed.Close())
		_, err = closed.LookupRelayJournalSeqs(ctx, []string{"x"})
		require.Error(t, err)
	})
}
