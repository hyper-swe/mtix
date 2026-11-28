// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package publisher_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// aheadRig publishes e1..e4 and returns the fake journal, the config
// and the medium's bytes: the state a kill -9 between the append and the
// cursor advance leaves, once the test rewinds the cursor.
func aheadRig(t *testing.T) (*fakeJournal, publisher.Config) {
	t.Helper()
	fake := &fakeJournal{
		pos: sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1},
		rows: []sqlite.RelayJournalEvent{
			fakeRow(1, "e1"), fakeRow(2, "e2"), fakeRow(3, "e3"), fakeRow(4, "e4"),
		},
	}
	cfg := fakeConfig(t, fake)
	p, err := publisher.New(cfg)
	require.NoError(t, err)
	n, err := p.PublishPending(context.Background())
	require.NoError(t, err)
	require.Equal(t, 4, n)
	return fake, cfg
}

func mediumEventIDs(t *testing.T, dir string) []string {
	t.Helper()
	r := &rig{segDir: dir}
	return r.publishedEvents(t)
}

// TestPublishPending_MediumAheadOfCursor_ResumesWithoutRepublishing is
// FR-21 §6.2 at the same pub_epoch: the medium holds 1..n records past
// the cursor and they are exactly the journal's next events.
func TestPublishPending_MediumAheadOfCursor_ResumesWithoutRepublishing(t *testing.T) {
	for _, ahead := range []int{1, 2, 3, 4} {
		for _, batch := range []int{0, 1} { // 1: the ahead run exceeds one tick's read
			t.Run(fmt.Sprintf("ahead_%d_batch_%d", ahead, batch), func(t *testing.T) {
				fake, cfg := aheadRig(t)
				fake.pos = sqlite.RelayPushPosition{Seq: int64(4 - ahead), PubEpoch: 1, NextRS: uint64(4 - ahead + 1)}
				cfg.BatchLimit = batch

				p, err := publisher.New(cfg)
				require.NoError(t, err)
				_, err = p.PublishPending(context.Background())
				require.NoError(t, err, "a crash in the §6.2 window must resume with no operator step")
				require.False(t, p.Diverged())
				require.Equal(t, []string{"e1", "e2", "e3", "e4"}, mediumEventIDs(t, cfg.SegmentsDir),
					"every event is on the medium exactly once")
				require.Equal(t, int64(4), fake.pos.Seq, "the cursor fast-forwards over the adopted records")
				require.Equal(t, uint64(5), fake.pos.NextRS)

				fake.rows = append(fake.rows, fakeRow(5, "e5"))
				p2, err := publisher.New(cfg)
				require.NoError(t, err)
				n, err := p2.PublishPending(context.Background())
				require.NoError(t, err)
				require.Equal(t, 1, n)
				require.Equal(t, []string{"e1", "e2", "e3", "e4", "e5"}, mediumEventIDs(t, cfg.SegmentsDir))
			})
		}
	}
}

// TestPublishPending_MediumAheadWithOtherRecords_StillRefuses keeps the
// §5.2 refusal for extra records that are not the journal's next events.
func TestPublishPending_MediumAheadWithOtherRecords_StillRefuses(t *testing.T) {
	tests := map[string]func(f *fakeJournal){
		"cursor claims a different position": func(f *fakeJournal) {
			f.pos = sqlite.RelayPushPosition{Seq: 0, PubEpoch: 1, NextRS: 2} // rs 2 is e2, next event is e1
		},
		"same event ids, different content": func(f *fakeJournal) {
			f.pos = sqlite.RelayPushPosition{Seq: 2, PubEpoch: 1, NextRS: 3}
			f.rows[2].Event.NodeID = "PROJ-99" // e3 in the journal is not what the medium holds
		},
		"middle ahead record differs by node id": func(f *fakeJournal) {
			f.pos = sqlite.RelayPushPosition{Seq: 1, PubEpoch: 1, NextRS: 2} // e2, e3, e4 are ahead
			f.rows[2].Event.NodeID = "PROJ-99"
		},
		"last ahead record differs by node id": func(f *fakeJournal) {
			f.pos = sqlite.RelayPushPosition{Seq: 1, PubEpoch: 1, NextRS: 2}
			f.rows[3].Event.NodeID = "PROJ-99"
		},
		"middle ahead record differs by payload": func(f *fakeJournal) {
			f.pos = sqlite.RelayPushPosition{Seq: 1, PubEpoch: 1, NextRS: 2}
			f.rows[2].Event.Payload = json.RawMessage(`{"x":1}`)
		},
		"last ahead record differs by payload": func(f *fakeJournal) {
			f.pos = sqlite.RelayPushPosition{Seq: 0, PubEpoch: 1, NextRS: 1}
			f.rows[3].Event.Payload = json.RawMessage(`{"x":1}`)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fake, cfg := aheadRig(t)
			before, err := os.ReadFile(filepath.Join(cfg.SegmentsDir, segment.FileName(1)))
			require.NoError(t, err)
			mutate(fake)
			pos := fake.pos

			p, err := publisher.New(cfg)
			require.NoError(t, err)
			_, err = p.PublishPending(context.Background())
			require.ErrorIs(t, err, segment.ErrPeerIDConflict)
			require.Equal(t, "RELAY_PEER_ID_CONFLICT", segment.CodeOf(err))
			require.Equal(t, pos, fake.pos, "a refusal leaves the cursor alone")
			after, err := os.ReadFile(filepath.Join(cfg.SegmentsDir, segment.FileName(1)))
			require.NoError(t, err)
			require.Equal(t, before, after, "a refusal writes nothing")
		})
	}
}

// A journal holding fewer events than the medium holds records past the
// cursor cannot vouch for them; the refusal must come without indexing
// past the end of the journal's rows.
func TestPublishPending_JournalShorterThanTheMedium_RefusesWithoutPanic(t *testing.T) {
	fake, cfg := aheadRig(t)
	fake.pos = sqlite.RelayPushPosition{Seq: 1, PubEpoch: 1, NextRS: 2} // the medium is 3 records ahead

	cfg.Journal = &shortReadJournal{fakeJournal: fake} // the journal yields one event where three are needed

	p, err := publisher.New(cfg)
	require.NoError(t, err)
	require.NotPanics(t, func() { _, err = p.PublishPending(context.Background()) })
	require.ErrorIs(t, err, segment.ErrPeerIDConflict)
	require.Equal(t, "RELAY_PEER_ID_CONFLICT", segment.CodeOf(err))
	require.Contains(t, err.Error(), "the medium holds 3 records past the cursor but the journal has 1 events")
}

// The divergence diagnostic names the journal position and renders the
// event id through the sanctioned renderer, so an operator can find the
// event without hostile text reaching the terminal raw.
func TestPublishPending_MediumAheadWithDifferentContent_NamesEventAndJournalSeq(t *testing.T) {
	fake, cfg := aheadRig(t)
	fake.pos = sqlite.RelayPushPosition{Seq: 2, PubEpoch: 1, NextRS: 3}
	fake.rows[2].Event.NodeID = "PROJ-99"

	p, err := publisher.New(cfg)
	require.NoError(t, err)
	_, err = p.PublishPending(context.Background())
	require.ErrorIs(t, err, segment.ErrPeerIDConflict)
	require.Contains(t, err.Error(), "journal seq 3")
	require.Contains(t, err.Error(), "event "+format.EventID("e3"))
}

// Reading the journal failed inside the verifier: that is not evidence of
// a second writer and must keep its own cause instead of
// RELAY_PEER_ID_CONFLICT. The first read (the batch read) succeeds so the
// writer and its AdoptAhead hook are reached; only the verifier's read fails.
func TestPublishPending_JournalReadFailsWhileVerifying_IsNotAPeerIDConflict(t *testing.T) {
	fake, cfg := aheadRig(t)
	fake.pos = sqlite.RelayPushPosition{Seq: 2, PubEpoch: 1, NextRS: 3}
	boom := errors.New("journal unavailable")
	j := &failNthReadJournal{fakeJournal: fake, failOn: 2, err: boom}
	cfg.Journal = j

	p, err := publisher.New(cfg)
	require.NoError(t, err)
	_, err = p.PublishPending(context.Background())
	require.Equal(t, 2, j.reads, "the verifier's journal read was reached")
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, segment.ErrPeerIDConflict)
	require.NotErrorIs(t, err, segment.ErrAheadMismatch)
	require.NotEqual(t, "RELAY_PEER_ID_CONFLICT", segment.CodeOf(err))
}

// Integers beyond 2^53 must compare exactly: two payloads that differ
// only past float64 precision are different events.
func TestPublishPending_MediumAheadWithLargeIntegerDifference_StillRefuses(t *testing.T) {
	fake := &fakeJournal{
		pos:  sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1},
		rows: []sqlite.RelayJournalEvent{fakeRow(1, "e1"), fakeRow(2, "e2")},
	}
	fake.rows[1].Event.Payload = json.RawMessage(`{"n":9007199254740992}`)
	cfg := fakeConfig(t, fake)
	p, err := publisher.New(cfg)
	require.NoError(t, err)
	_, err = p.PublishPending(context.Background())
	require.NoError(t, err)

	fake.pos = sqlite.RelayPushPosition{Seq: 1, PubEpoch: 1, NextRS: 2}
	fake.rows[1].Event.Payload = json.RawMessage(`{"n":9007199254740993}`) // equal as a float64
	p2, err := publisher.New(cfg)
	require.NoError(t, err)
	_, err = p2.PublishPending(context.Background())
	require.ErrorIs(t, err, segment.ErrPeerIDConflict)
}

// shortReadJournal answers a read with at most one event, as a journal
// holding fewer events than the medium holds records would.
type shortReadJournal struct{ *fakeJournal }

func (j *shortReadJournal) ReadRelayJournalSince(ctx context.Context, seq int64, limit int) ([]sqlite.RelayJournalEvent, error) {
	return j.fakeJournal.ReadRelayJournalSince(ctx, seq, 1)
}

// failNthReadJournal fails only the failOn-th ReadRelayJournalSince call.
type failNthReadJournal struct {
	*fakeJournal
	failOn int
	reads  int
	err    error
}

func (j *failNthReadJournal) ReadRelayJournalSince(ctx context.Context, seq int64, limit int) ([]sqlite.RelayJournalEvent, error) {
	j.reads++
	if j.reads == j.failOn {
		return nil, j.err
	}
	return j.fakeJournal.ReadRelayJournalSince(ctx, seq, limit)
}

// A medium record whose payload is the journal event's JSON followed by
// extra bytes is not that event; adoption refuses it.
func TestPublishPending_MediumAheadWithTrailingBytes_StillRefuses(t *testing.T) {
	fake := &fakeJournal{
		pos: sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1},
	}
	// ten events: the record with trailing bytes (the 2nd) lies outside the
	// publisher's 8-record tail check, so adoption itself must catch it.
	for i := 1; i <= 10; i++ {
		fake.rows = append(fake.rows, fakeRow(int64(i), fmt.Sprintf("e%d", i)))
	}
	cfg := fakeConfig(t, fake)
	w, err := segment.NewWriter(segment.WriterConfig{
		Dir: cfg.SegmentsDir, PeerID: cfg.PeerID, Key: cfg.Key, KeyEpoch: cfg.KeyEpoch,
		PubEpoch: 1, NextRS: 1,
	})
	require.NoError(t, err)
	for i, row := range fake.rows {
		payload, mErr := json.Marshal(row.Event)
		require.NoError(t, mErr)
		if i == 1 {
			payload = append(payload, []byte(` {"x":1}`)...)
		}
		_, aErr := w.Append(payload)
		require.NoError(t, aErr)
	}
	require.NoError(t, w.Close())
	fake.pos = sqlite.RelayPushPosition{Seq: 1, PubEpoch: 1, NextRS: 2}
	before, err := os.ReadFile(filepath.Join(cfg.SegmentsDir, segment.FileName(1)))
	require.NoError(t, err)

	p, err := publisher.New(cfg)
	require.NoError(t, err)
	_, err = p.PublishPending(context.Background())
	require.ErrorIs(t, err, segment.ErrPeerIDConflict)
	require.Equal(t, int64(1), fake.pos.Seq, "a refusal leaves the cursor alone")
	after, err := os.ReadFile(filepath.Join(cfg.SegmentsDir, segment.FileName(1)))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

// A cursor advance that fails while fast-forwarding over adopted records
// is returned and counted, not swallowed.
func TestPublishPending_AdvanceFailsAfterAdoption_IsReturnedAndCounted(t *testing.T) {
	fake, cfg := aheadRig(t)
	fake.pos = sqlite.RelayPushPosition{Seq: 2, PubEpoch: 1, NextRS: 3} // 2 adopted, no new rows
	boom := errors.New("cursor write failed")
	fake.errAdvance = boom

	p, err := publisher.New(cfg)
	require.NoError(t, err)
	n, err := p.PublishPending(context.Background())
	require.ErrorIs(t, err, boom)
	require.Equal(t, 0, n)
	require.Equal(t, int64(1), p.Stats().Failures)
	require.Equal(t, int64(2), fake.pos.Seq, "the cursor did not move")
}
