// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func markCount(t *testing.T, s *sqlite.Store, agent string) int {
	t.Helper()
	var n int
	require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM agent_inbox_cursor_marks WHERE agent_id=?`, agent).Scan(&n))
	return n
}

func requireAckState(t *testing.T, s *sqlite.Store, seq int64, kind string, known bool, at time.Time) {
	t.Helper()
	info, err := s.InboxAckInfo(t.Context(), "w", seq)
	require.NoError(t, err)
	require.Equal(t, kind, info.Kind, "seq %d", seq)
	require.Equal(t, known, info.TimeKnown, "seq %d", seq)
	if known {
		require.NotNil(t, info.AckedAt)
		require.WithinDuration(t, at, *info.AckedAt, time.Nanosecond, "seq %d", seq)
	} else {
		require.Nil(t, info.AckedAt, "seq %d", seq)
	}
}

// A watermark ack made before the marks existed, or whose mark was evicted,
// must report "acked, time unknown", never a later mark's time (review F1).
func TestInboxAckInfo_UncoveredWatermarkAcks_ReportUnknownNeverALaterTime(t *testing.T) {
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		run  func(t *testing.T, s *sqlite.Store, cur *time.Time, seqs []int64)
	}{
		{"re-ack of a lower seq after a pre-table ack", func(t *testing.T, s *sqlite.Store, cur *time.Time, seqs []int64) {
			_, err := s.WriteDB().Exec(`INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES('w',?)`, seqs[2])
			require.NoError(t, err)
			*cur = cur.Add(24 * time.Hour)
			require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[1]))
			require.Zero(t, markCount(t, s, "w"), "a re-ack that does not advance the cursor writes no mark")
			for _, q := range seqs[:3] {
				requireAckState(t, s, q, sqlite.AckWatermark, false, time.Time{})
			}
		}},
		{"later advance after a pre-table ack", func(t *testing.T, s *sqlite.Store, cur *time.Time, seqs []int64) {
			_, err := s.WriteDB().Exec(`INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES('w',?)`, seqs[1])
			require.NoError(t, err)
			*cur = cur.Add(time.Hour)
			require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[3]))
			requireAckState(t, s, seqs[0], sqlite.AckWatermark, false, time.Time{})
			requireAckState(t, s, seqs[1], sqlite.AckWatermark, false, time.Time{})
			requireAckState(t, s, seqs[2], sqlite.AckWatermark, true, *cur)
			requireAckState(t, s, seqs[3], sqlite.AckWatermark, true, *cur)
		}},
		{"evicted mark with a higher mark present", func(t *testing.T, s *sqlite.Store, cur *time.Time, seqs []int64) {
			require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[1]))
			*cur = cur.Add(time.Hour)
			require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[3]))
			_, err := s.WriteDB().Exec(`DELETE FROM agent_inbox_cursor_marks WHERE agent_id='w' AND cursor=?`, seqs[1])
			require.NoError(t, err)
			requireAckState(t, s, seqs[0], sqlite.AckWatermark, false, time.Time{})
			requireAckState(t, s, seqs[1], sqlite.AckWatermark, false, time.Time{})
			requireAckState(t, s, seqs[2], sqlite.AckWatermark, true, *cur)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			clock, cur := ticking(t0)
			s.SetClock(clock)
			seqs := seedAddressed(t, s, "w", 4)
			tt.run(t, s, cur, seqs)
		})
	}
}

// A selective ack later covered by --through keeps its kind and time (review F3),
// and a re-ack of it never moves the recorded time.
func TestInboxAckInfo_SelectiveThenThrough_KeepsKindAndTime(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	clock, cur := ticking(t0)
	s.SetClock(clock)
	seqs := seedAddressed(t, s, "w", 3)
	require.NoError(t, s.InboxAck(t.Context(), "w", seqs[0]))
	*cur = t0.Add(time.Hour)
	require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[2]))
	requireAckState(t, s, seqs[0], sqlite.AckSelective, true, t0)
	requireAckState(t, s, seqs[1], sqlite.AckWatermark, true, t0.Add(time.Hour))
	requireAckState(t, s, seqs[2], sqlite.AckWatermark, true, t0.Add(time.Hour))

	// Re-acking it later, then folding again, keeps the first time.
	*cur = t0.Add(2 * time.Hour)
	require.NoError(t, s.InboxAck(t.Context(), "w", seqs[0]))
	require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[2]))
	requireAckState(t, s, seqs[0], sqlite.AckSelective, true, t0)
}

// When the bound evicts a folded selective ack's mark it reports unknown, not the
// fold time.
func TestInboxAckInfo_FoldedSelectiveEvicted_ReportsUnknown(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	clock, cur := ticking(t0)
	s.SetClock(clock)
	seqs := seedAddressed(t, s, "w", 2)
	require.NoError(t, s.InboxAck(t.Context(), "w", seqs[0]))
	*cur = t0.Add(time.Hour)
	require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[1]))
	for i := 0; i < sqlite.MaxCursorMarks; i++ {
		c := 1000 + i
		_, err := s.WriteDB().Exec(`INSERT INTO agent_inbox_cursor_marks(agent_id,cursor,from_cursor,kind,advanced_at)
			VALUES('w',?,?,'watermark','2026-11-11T12:00:00.000000000Z')`, c, c-1)
		require.NoError(t, err)
	}
	*cur = t0.Add(2 * time.Hour)
	_, err := s.WriteDB().Exec(`UPDATE agent_inbox_cursor SET cursor=cursor-1 WHERE agent_id='w'`)
	require.NoError(t, err)
	require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[1]))
	require.LessOrEqual(t, markCount(t, s, "w"), sqlite.MaxCursorMarks)
	requireAckState(t, s, seqs[0], sqlite.AckWatermark, false, time.Time{})
	requireAckState(t, s, seqs[1], sqlite.AckWatermark, false, time.Time{})
}

func TestDiscardLocal_ClearsCursorMarks(t *testing.T) {
	s := newTestStore(t)
	seqs := seedAddressed(t, s, "w", 2)
	require.NoError(t, s.InboxAck(t.Context(), "w", seqs[0]))
	require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[1]))
	require.Positive(t, markCount(t, s, "w"))
	require.NoError(t, sqlite.DiscardLocal(t.Context(), s, discardMtixDir(t)))
	require.Zero(t, markCount(t, s, "w"), "marks are keyed to reusable rowids and must go with the journal")
}

func TestInboxRetention_ReplaceTrim_TrimsCursorMarksToTheTail(t *testing.T) {
	ctx := context.Background()
	s, raw := retentionStore(t)
	addressTo(t, s, "bea", "PROJ-1")
	addressTo(t, s, "bea", "PROJ-2")
	addressTo(t, s, "ann", "PROJ-3")
	bea, err := s.InboxList(ctx, "bea")
	require.NoError(t, err)
	ann, err := s.InboxList(ctx, "ann")
	require.NoError(t, err)
	require.NoError(t, s.InboxAckThrough(ctx, "bea", bea[0].Seq))
	require.NoError(t, s.InboxAckThrough(ctx, "ann", ann[0].Seq))
	tail := bea[1].Seq
	// Marks wholly above the tail (a stale watermark and selective one).
	for _, kind := range []string{"watermark", "selective"} {
		_, err = raw.Exec(`INSERT INTO agent_inbox_cursor_marks(agent_id,cursor,from_cursor,kind,advanced_at)
			VALUES('ann',?,?,?,'2026-11-11T12:00:00.000000000Z')`, tail+5, tail+4, kind)
		require.NoError(t, err)
	}
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	data = withoutExportNode(data, "PROJ-3")
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	_, err = s.Import(ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	var maxCursor, rows int64
	require.NoError(t, raw.QueryRow(`SELECT COALESCE(MAX(cursor),0), COUNT(*) FROM agent_inbox_cursor_marks WHERE agent_id='ann'`).Scan(&maxCursor, &rows))
	require.Equal(t, tail, maxCursor, "the straddling mark is clipped to the surviving tail")
	require.Equal(t, int64(1), rows, "marks wholly above the tail are gone")
	var beaMax int64
	require.NoError(t, raw.QueryRow(`SELECT MAX(cursor) FROM agent_inbox_cursor_marks WHERE agent_id='bea'`).Scan(&beaMax))
	require.Equal(t, bea[0].Seq, beaMax, "a mark below the tail is left alone")
}

// Each wait owns its row, so the first to exit never clears a live second wait
// (review F4).
func TestInboxWait_OverlappingWaitsInOneProcess_EachOwnsItsRow(t *testing.T) {
	s := newTestStore(t)
	short, long := make(chan struct{}), make(chan struct{})
	longCtx, cancelLong := context.WithCancel(t.Context())
	defer cancelLong()
	go func() { defer close(short); _, _ = s.InboxWait(t.Context(), "w", 300*time.Millisecond) }()
	go func() { defer close(long); _, _ = s.InboxWait(longCtx, "w", 0) }()
	rows := func() int {
		var n int
		require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM agent_inbox_waiters WHERE agent_id='w'`).Scan(&n))
		return n
	}
	require.Eventually(t, func() bool { return rows() == 2 }, 2*time.Second, 10*time.Millisecond)
	<-short
	p, err := s.InboxParked(t.Context(), "w")
	require.NoError(t, err)
	require.True(t, p.Parked, "the second wait is still live")
	require.Equal(t, 1, rows())
	cancelLong()
	<-long
	require.Zero(t, rows())
}

func TestInboxParked_TwoLiveWaiters_SinceIsTheEarliest(t *testing.T) {
	s := newTestStore(t)
	start, err := procinfo.StartTime(os.Getppid())
	require.NoError(t, err)
	for i, since := range []string{"2026-11-11T10:00:00.000000000Z", "2026-11-11T09:00:00.000000000Z"} {
		_, err := s.WriteDB().Exec(`INSERT INTO agent_inbox_waiters(agent_id,pid,pid_start_time,token,since) VALUES('w',?,?,?,?)`,
			os.Getppid(), start, i+1, since)
		require.NoError(t, err)
	}
	p, err := s.InboxParked(t.Context(), "w")
	require.NoError(t, err)
	require.True(t, p.Parked)
	require.Equal(t, time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC), p.Since.UTC())
	require.Equal(t, []int64{int64(os.Getppid())}, p.PIDs, "one pid is listed once")
}

// Removal is exact: only the wait's own (pid, start, token) row goes.
func TestInboxWait_Exit_KeepsRowWithSamePidAndTokenButOtherStart(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); _, _ = s.InboxWait(ctx, "w", 0) }()
	var pid, token int64
	require.Eventually(t, func() bool {
		return s.WriteDB().QueryRow(`SELECT pid, token FROM agent_inbox_waiters WHERE agent_id='w'`).Scan(&pid, &token) == nil
	}, 2*time.Second, 10*time.Millisecond)
	_, err := s.WriteDB().Exec(`INSERT INTO agent_inbox_waiters(agent_id,pid,pid_start_time,token,since) VALUES('w',?,?,?,?)`,
		pid, "other-start", token, "2026-11-11T09:00:00.000000000Z")
	require.NoError(t, err)
	cancel()
	<-done
	var n int
	require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM agent_inbox_waiters WHERE pid_start_time='other-start'`).Scan(&n))
	require.Equal(t, 1, n)
}

// The dead-row prune and the new row are one transaction: a failed insert
// rolls the prune back (review F5).
func TestInboxWait_FailedWaiterInsert_RollsBackThePrune(t *testing.T) {
	s := newTestStore(t)
	_, err := s.WriteDB().Exec(`INSERT INTO agent_inbox_waiters(agent_id,pid,pid_start_time,token,since) VALUES('w',2147483646,'x',1,'2026-11-11T09:00:00.000000000Z')`)
	require.NoError(t, err)
	_, err = s.WriteDB().Exec(`CREATE TRIGGER block_waiter_insert BEFORE INSERT ON agent_inbox_waiters
		WHEN NEW.pid <> 2147483646 BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
	require.NoError(t, err)
	_, err = s.InboxWait(t.Context(), "w", 20*time.Millisecond)
	require.NoError(t, err, "a refused waiter write never fails the wait")
	var n int
	require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM agent_inbox_waiters WHERE pid=2147483646`).Scan(&n))
	require.Equal(t, 1, n, "the prune rolled back with the failed insert")
}

func TestInboxWait_EmptyAgent_RejectedBeforeAnyDeliveryWrite(t *testing.T) {
	s := newTestStore(t)
	_, err := s.InboxWait(t.Context(), "", 20*time.Millisecond)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	for _, tbl := range []string{"agent_inbox_reads", "agent_inbox_waiters"} {
		var n int
		require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM `+tbl).Scan(&n)) // test-only constant identifiers
		require.Zero(t, n, tbl)
	}
}

// A wait stamps last read when it starts and when it ends, on every exit path.
func TestInboxWait_StampsLastReadAtEntryAndExit_OnEveryExitPath(t *testing.T) {
	far := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		seed   bool
		cancel bool
	}{
		{"timeout", false, false},
		{"event arrives", true, false},
		{"caller cancels", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			if tt.seed {
				seedAddressed(t, s, "w", 1)
				_, err := s.WriteDB().Exec(`DELETE FROM agent_inbox_ack`)
				require.NoError(t, err)
			}
			_, err := s.WriteDB().Exec(`DELETE FROM agent_inbox_reads`)
			require.NoError(t, err)
			steps := 0
			s.SetClock(func() time.Time { steps++; return far.Add(time.Duration(steps-1) * time.Minute) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			timeout := 400 * time.Millisecond
			if tt.cancel {
				timeout = 0
			}
			done := make(chan struct{})
			go func() { defer close(done); _, _ = s.InboxWait(ctx, "w", timeout) }()
			if !tt.seed {
				require.Eventually(t, func() bool { p, perr := s.InboxParked(t.Context(), "w"); return perr == nil && p.Parked }, 2*time.Second, 5*time.Millisecond)
				entry, lerr := s.InboxLastRead(t.Context(), "w")
				require.NoError(t, lerr)
				require.NotNil(t, entry, "stamped on entry, before the wait blocks")
				require.True(t, entry.Equal(far), "entry stamp is the first clock read")
			}
			if tt.cancel {
				cancel()
			}
			<-done
			exit, lerr := s.InboxLastRead(t.Context(), "w")
			require.NoError(t, lerr)
			require.NotNil(t, exit)
			require.True(t, exit.After(far.Add(time.Minute)), "exit stamp is a later clock read, got %v", exit)
		})
	}
}

// Eviction defines "oldest" as the lowest cursor (the earliest event), not the
// oldest advanced_at. A folded selective mark with the HIGHER cursor and the OLDER
// time competes with a watermark mark with the LOWER cursor and the NEWER time for
// the last slot of the bound: the lower cursor goes, whatever the times say.
func TestInboxMarks_Eviction_DropsTheLowestCursorNotTheOldestTime(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	clock, cur := ticking(t0)
	s.SetClock(clock)
	seqs := seedAddressed(t, s, "w", 3)
	older, newer := "2026-11-11T08:00:00.000000000Z", "2026-11-11T12:00:00.000000000Z"
	insert := func(cursor, from int64, kind, at string) {
		_, err := s.WriteDB().Exec(`INSERT INTO agent_inbox_cursor_marks(agent_id,cursor,from_cursor,kind,advanced_at)
			VALUES('w',?,?,?,?)`, cursor, from, kind, at)
		require.NoError(t, err)
	}
	_, err := s.WriteDB().Exec(`INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES('w',?)`, seqs[1])
	require.NoError(t, err)
	insert(seqs[0], 0, sqlite.AckWatermark, newer)         // lower cursor, newer time
	insert(seqs[1], seqs[1]-1, sqlite.AckSelective, older) // higher cursor, older time
	for i := 0; i < sqlite.MaxCursorMarks-2; i++ {
		c := int64(1000 + i)
		insert(c, c-1, sqlite.AckWatermark, newer)
	}
	require.Equal(t, sqlite.MaxCursorMarks, markCount(t, s, "w"))

	*cur = t0.Add(2 * time.Hour)
	require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[2])) // one mark too many
	require.Equal(t, sqlite.MaxCursorMarks, markCount(t, s, "w"))

	var lowest int64
	var kind string
	require.NoError(t, s.WriteDB().QueryRow(`SELECT cursor, kind FROM agent_inbox_cursor_marks WHERE agent_id='w'
		ORDER BY cursor ASC LIMIT 1`).Scan(&lowest, &kind))
	require.Equal(t, seqs[1], lowest, "the lowest-cursor mark (the newer watermark one) was evicted")
	require.Equal(t, sqlite.AckSelective, kind, "the older selective mark at the higher cursor survives")
	requireAckState(t, s, seqs[0], sqlite.AckWatermark, false, time.Time{})
	requireAckState(t, s, seqs[2], sqlite.AckWatermark, true, t0.Add(2*time.Hour))
}
