// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"errors"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

func at(segNo, rs uint64, epoch uint16) ingest.Position {
	return ingest.Position{SegmentNo: segNo, RS: rs, PubEpoch: epoch}
}

func reached(segNo, rs uint64, epoch uint16) segment.Cursor {
	return segment.Cursor{SegmentNo: segNo, RS: rs, PubEpoch: epoch}
}

// TestNext_CondemnedSegmentNeverMovesTheCursor is the rule the shared
// corruption corpus makes concrete, and the one a reader cannot get
// wrong twice.
//
// A sealed segment DELIVERS its clean prefix and is THEN condemned —
// the corpus pins exactly that pairing. Banking the prefix would step
// the reader over whatever the damage concealed, which is the silent
// hole §5.4 exists to refuse. The records were applied idempotently, so
// re-reading them next poll costs nothing; the unmoved cursor is what
// keeps the stall visible until an operator repairs it.
func TestNext_CondemnedSegmentNeverMovesTheCursor(t *testing.T) {
	prev := at(2, 50, 1)

	tests := []struct {
		name    string
		outcome ingest.Outcome
	}{
		{"corrupt with a clean prefix delivered", ingest.Outcome{
			Delivered: 2, Reached: reached(3, 52, 1), Verdict: segment.ErrSegmentCorrupt,
		}},
		{"corrupt with nothing delivered", ingest.Outcome{
			Delivered: 0, Reached: reached(3, 50, 1), Verdict: segment.ErrSegmentCorrupt,
		}},
		{"a contiguity gap mid-segment", ingest.Outcome{
			Delivered: 5, Reached: reached(3, 55, 1), Verdict: segment.ErrGap,
		}},
		{"a MAC failure after a long valid prefix", ingest.Outcome{
			Delivered: 100, Reached: reached(3, 150, 1), Verdict: segment.ErrMACMismatch,
		}},
		{"an unclassified medium error", ingest.Outcome{
			Delivered: 3, Reached: reached(3, 53, 1), Verdict: errors.New("input/output error"),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, moved := ingest.Next(prev, tt.outcome)
			require.False(t, moved, "a verdict must never move the cursor")
			require.Equal(t, prev, got)
		})
	}
}

// TestNext_CleanScanAdvances covers the ordinary forward cases.
func TestNext_CleanScanAdvances(t *testing.T) {
	tests := []struct {
		name    string
		prev    ingest.Position
		outcome ingest.Outcome
		want    ingest.Position
	}{
		{"first records of a fresh stream", at(0, 0, 0),
			ingest.Outcome{Delivered: 3, Reached: reached(1, 3, 0)}, at(1, 3, 0)},
		{"more records in the same segment", at(1, 3, 1),
			ingest.Outcome{Delivered: 2, Reached: reached(1, 5, 1)}, at(1, 5, 1)},
		{"across a rotation", at(1, 5, 1),
			ingest.Outcome{Delivered: 2, Reached: reached(2, 7, 1)}, at(2, 7, 1)},
		{"an empty sealed segment still moves the reader on", at(1, 5, 1),
			ingest.Outcome{Delivered: 0, Reached: reached(2, 5, 1)}, at(2, 5, 1)},
		{"an active tail truncated mid-append still banks its prefix", at(2, 7, 1),
			ingest.Outcome{Delivered: 2, Reached: reached(2, 9, 1), Truncated: true}, at(2, 9, 1)},
		{"a publisher epoch bump restarts relay sequences", at(4, 900, 1),
			ingest.Outcome{Delivered: 3, Reached: reached(5, 3, 2)}, at(5, 3, 2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, moved := ingest.Next(tt.prev, tt.outcome)
			require.True(t, moved)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestNext_RefusesEveryBackwardMove is the complement, enumerated. Each
// case is a way a reader could be walked backwards into re-applying or
// re-exposing history, and every one leaves the cursor where it was.
func TestNext_RefusesEveryBackwardMove(t *testing.T) {
	tests := []struct {
		name    string
		prev    ingest.Position
		outcome ingest.Outcome
	}{
		{"a replayed relay sequence", at(2, 50, 1),
			ingest.Outcome{Delivered: 1, Reached: reached(2, 10, 1)}},
		{"the same position again", at(2, 50, 1),
			ingest.Outcome{Delivered: 0, Reached: reached(2, 50, 1)}},
		{"an earlier segment at the same position", at(3, 50, 1),
			ingest.Outcome{Delivered: 0, Reached: reached(2, 50, 1)}},
		{"an epoch rollback, however high its relay sequence", at(5, 3, 2),
			ingest.Outcome{Delivered: 9, Reached: reached(6, 9000, 1)}},
		{"an epoch rollback to the very beginning", at(5, 3, 2),
			ingest.Outcome{Delivered: 1, Reached: reached(1, 1, 0)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, moved := ingest.Next(tt.prev, tt.outcome)
			require.False(t, moved)
			require.Equal(t, tt.prev, got)
		})
	}
}

// TestNext_TransitionsAreExhaustive walks the cross-product of epoch and
// relay-sequence relations against both scan outcomes, so every
// combination the reader can meet has a stated verdict rather than an
// emergent one. This is the FR-21 §9 state-machine treatment applied to
// the ingest cursor.
func TestNext_TransitionsAreExhaustive(t *testing.T) {
	prev := at(5, 100, 2)

	epochs := []struct {
		name  string
		epoch uint16
	}{
		{"older epoch", 1},
		{"same epoch", 2},
		{"newer epoch", 3},
	}
	sequences := []struct {
		name string
		rs   uint64
	}{
		{"lower rs", 50},
		{"same rs", 100},
		{"higher rs", 150},
	}
	segments := []struct {
		name  string
		segNo uint64
	}{
		{"earlier segment", 4},
		{"same segment", 5},
		{"later segment", 6},
	}
	verdicts := []struct {
		name    string
		verdict error
	}{
		{"clean", nil},
		{"condemned", segment.ErrSegmentCorrupt},
	}

	for _, e := range epochs {
		for _, s := range sequences {
			for _, sg := range segments {
				for _, v := range verdicts {
					t.Run(e.name+"/"+s.name+"/"+sg.name+"/"+v.name, func(t *testing.T) {
						got, moved := ingest.Next(prev, ingest.Outcome{
							Delivered: 1,
							Reached:   reached(sg.segNo, s.rs, e.epoch),
							Verdict:   v.verdict,
						})

						// A verdict overrides everything else.
						want := v.verdict == nil && (e.epoch > prev.PubEpoch ||
							(e.epoch == prev.PubEpoch &&
								(s.rs > prev.RS || (s.rs == prev.RS && sg.segNo > prev.SegmentNo))))

						require.Equal(t, want, moved)
						if !moved {
							require.Equal(t, prev, got)
							return
						}
						require.Equal(t, at(sg.segNo, s.rs, e.epoch), got)
					})
				}
			}
		}
	}
}

func filesNumbered(nos ...uint64) []segment.File {
	out := make([]segment.File, 0, len(nos))
	for _, n := range nos {
		out = append(out, segment.File{No: n, Name: segment.FileName(n)})
	}
	return out
}

func numbersOf(segs []segment.File) []uint64 {
	if len(segs) == 0 {
		return nil
	}
	out := make([]uint64, 0, len(segs))
	for _, s := range segs {
		out = append(out, s.No)
	}
	return out
}

// TestUnread_OpensOnlyWhatTheCursorHasNotPassed is the FR-21 §9 I/O
// bound stated as a property: a poll opens the segment holding the
// cursor — so a reader can resume inside it — and everything above,
// never anything below. That is what keeps a tick's cost proportional
// to new work rather than to the length of history.
func TestUnread_OpensOnlyWhatTheCursorHasNotPassed(t *testing.T) {
	all := filesNumbered(1, 2, 3, 4, 5)

	tests := []struct {
		name string
		prev ingest.Position
		want []uint64
	}{
		{"a fresh reader opens everything", at(0, 0, 0), []uint64{1, 2, 3, 4, 5}},
		{"mid-stream opens the current segment and up", at(3, 40, 1), []uint64{3, 4, 5}},
		{"caught up opens only the active tail", at(5, 90, 1), []uint64{5}},
		{"past the tail opens nothing", at(9, 90, 1), nil},
		{"a pruned floor skips what is gone", at(2, 10, 1), []uint64{2, 3, 4, 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, numbersOf(ingest.Unread(all, tt.prev)))
		})
	}
}

// TestUnread_CostDoesNotGrowWithHistory is the same bound checked the
// way it actually matters: with the reader caught up, adding thousands
// of sealed segments behind it must not add a single file to the poll.
func TestUnread_CostDoesNotGrowWithHistory(t *testing.T) {
	for _, history := range []int{10, 100, 5000} {
		nos := make([]uint64, 0, history)
		for i := 1; i <= history; i++ {
			nos = append(nos, uint64(i))
		}
		segs := filesNumbered(nos...)
		caughtUp := at(uint64(history), 1, 1)

		require.Len(t, ingest.Unread(segs, caughtUp), 1,
			"a caught-up reader opens one segment regardless of %d segments of history", history)
	}
}

// TestUnread_HandlesAnEpochBump: reset-peer republishes into fresh,
// higher-numbered segments, so the cursor's segment stays the floor.
func TestUnread_HandlesAnEpochBump(t *testing.T) {
	segs := filesNumbered(1, 2, 3, 4)
	require.Equal(t, []uint64{3, 4}, numbersOf(ingest.Unread(segs, at(3, 1, 2))))
}

// TestUnread_EmptyDirectory covers a peer that has published nothing.
func TestUnread_EmptyDirectory(t *testing.T) {
	require.Empty(t, ingest.Unread(nil, at(0, 0, 0)))
}
