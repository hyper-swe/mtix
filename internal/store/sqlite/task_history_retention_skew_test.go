// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const skewTestLayout = "2006-01-02T15:04:05.000000000Z"

// An entry dated ahead of the store clock but inside the 24 h window is stored
// and never trimmed, so a re-import of the same file adds nothing and writes
// no further marker until the clock passes it (MTIX-95.31.19.4).
func TestRetention_EntriesInsideTheSkewWindow_AreNeverTrimmedOrResurrected(t *testing.T) {
	f := retentionFixture(t, 2)
	f.bump(t, 1)
	f.s.SetHistoryExportSource(func() bool { return true })
	data, err := f.s.Export(context.Background(), "PROJ", "0.6.0")
	require.NoError(t, err)
	uid := f.rootUID(t)
	ahead := historyTestTime.Add(time.Hour).Format(skewTestLayout)
	var future []any
	for i := 0; i < 4; i++ {
		e := entry(fmt.Sprintf("01U0000000000000000000J%03d", i), uid, "status")
		e["ts"] = ahead
		future = append(future, e)
	}
	file := editFile(t, data, func(m map[string]any) {
		for _, n := range m["nodes"].([]any) {
			if n.(map[string]any)["uid"] == uid {
				n.(map[string]any)["history"] = future
			}
		}
	})
	importAheadCount := func() int {
		var n int
		require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE ts = ?`, ahead).Scan(&n))
		return n
	}
	for round := 0; round < 3; round++ {
		res := importMode(t, f.s, file, "merge")
		if round > 0 {
			assert.Zero(t, res.HistoryAdded, "round %d: a re-import adds nothing", round)
		}
		f.bump(t, 1) // over the limit: trims
		assert.Equal(t, 4, importAheadCount(), "round %d: no entry dated after the clock is dropped", round)
	}
	// Each marker stands for a real trim of entries dated at or before the clock.
	rows, err := f.db.Query(`SELECT new_value FROM task_history WHERE kind = 'retention_drop'`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		assert.NotEqual(t, "0", n)
	}
	require.NoError(t, rows.Err())
}

// The 24 h bound: at it is accepted, beyond it is skipped and counted, in both modes.
func TestImportHistory_SkewBound_ExactlyTwentyFourHoursAccepted_BeyondRefused(t *testing.T) {
	now := historyTestTime
	cases := []struct {
		name string
		ts   time.Time
		kept bool
	}{
		{"23h", now.Add(23 * time.Hour), true},
		{"exactly 24h", now.Add(24 * time.Hour), true},
		{"24h plus 1ns", now.Add(24*time.Hour + time.Nanosecond), false},
		{"25h", now.Add(25 * time.Hour), false},
	}
	for _, mode := range bothModes {
		for _, c := range cases {
			t.Run(string(mode)+"/"+c.name, func(t *testing.T) {
				file, uid := unionFile(t)
				e := entry("01U0000000000000000000K001", uid, "status")
				e["ts"] = c.ts.Format(skewTestLayout)
				file = editFile(t, file, func(m map[string]any) { node0(m)["history"] = []any{e} })
				dst, _ := unionDest(t)
				dst.s.SetClock(func() time.Time { return now })
				res := importMode(t, dst.s, file, mode)
				if c.kept {
					assert.Equal(t, 1, res.HistoryAdded)
					assert.Equal(t, []string{"01U0000000000000000000K001"}, historyIDs(t, dst.db))
					return
				}
				assert.Zero(t, res.HistoryAdded)
				assert.Equal(t, 1, res.HistorySkipped, "the refused entry is counted")
				assert.Empty(t, historyIDs(t, dst.db))
				assert.Contains(t, fmt.Sprint(res.Warnings), "in the future")
			})
		}
	}
}

// Oversize counts tasks with MORE change entries than the limit, and counts
// only change entries.
func TestHistorySizeReport_OversizeBoundaryAndKind(t *testing.T) {
	f := retentionFixture(t, 0)
	f.bump(t, 3)
	uid := f.rootUID(t)
	rep, err := f.s.HistorySize(context.Background(), 3, 3)
	require.NoError(t, err)
	assert.Zero(t, rep.Oversize, "exactly at the limit is not oversize")
	// Exempt rows push the row count over the limit but not the change count.
	for i := 0; i < 5; i++ {
		_, err := f.db.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, old_value, new_value, event_id)
			VALUES (?, ?, '2026-03-14T12:00:00.000000000Z', 'tester', 'system', 'redaction', 'x', NULL, NULL, NULL)`,
			fmt.Sprintf("01U0000000000000000000M%03d", i), uid)
		require.NoError(t, err)
	}
	rep, err = f.s.HistorySize(context.Background(), 3, 3)
	require.NoError(t, err)
	assert.Zero(t, rep.Oversize, "rows of other kinds do not count toward the limit")
	rep, err = f.s.HistorySize(context.Background(), 3, 2)
	require.NoError(t, err)
	assert.Equal(t, 1, rep.Oversize)
}

// Entries dated ahead of the clock are neither counted toward the limit nor
// dropped: a task holding many of them still keeps its own recent changes, and
// trims (and so markers) stay as rare as for any other task.
func TestRetention_ManyEntriesDatedAhead_DoNotEatTheLocalChanges(t *testing.T) {
	f := retentionFixture(t, 10)
	uid := f.rootUID(t)
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("01U0000000000000000000N%03d", i)
		insertHistory(t, f.db, id, uid, "status")
		_, err := f.db.Exec(`UPDATE task_history SET ts = ? WHERE entry_id = ?`,
			historyTestTime.Add(time.Hour).Format(skewTestLayout), id)
		require.NoError(t, err)
	}
	f.bump(t, 30)
	var local, ahead int
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE kind = 'change' AND ts <= ?`,
		historyTestTime.Add(time.Hour).Format(skewTestLayout)).Scan(&local))
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE kind = 'change' AND entry_id LIKE '01U0000000000000000000N%'`).Scan(&ahead))
	assert.Equal(t, 20, ahead, "entries dated ahead are never dropped")
	assert.Equal(t, 10, local-20, "the local changes survive at the limit")
	assert.Equal(t, 10, f.count(t, "retention_drop"), "the first trim at the 11th change, then one every second change")
	var other int
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE kind = 'retention_drop' AND new_value <> '2'`).Scan(&other))
	assert.Zero(t, other, "every trim drops exactly 2 entries")
}

// The size report counts entries dated ahead of the clock apart from the
// limit, and an oversize count that leaves them out, as the trim does.
func TestHistorySizeReport_EntriesDatedAhead_AreReportedAndNotCountedOversize(t *testing.T) {
	f := retentionFixture(t, 0)
	f.bump(t, 2)
	uid := f.rootUID(t)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("01U0000000000000000000P%03d", i)
		insertHistory(t, f.db, id, uid, "status")
		_, err := f.db.Exec(`UPDATE task_history SET ts = ? WHERE entry_id = ?`, historyTestTime.Add(time.Hour).Format(skewTestLayout), id)
		require.NoError(t, err)
	}
	rep, err := f.s.HistorySize(context.Background(), 3, 3)
	require.NoError(t, err)
	assert.Equal(t, 5, rep.Ahead)
	assert.Zero(t, rep.Oversize, "only the 2 entries at or before the clock count")
}

// A cutoff recorded while this machine's clock ran ahead must not outlive the
// correction of the clock: a peer entry dated just after the true now is kept.
func TestImportHistory_CutoffRecordedWhileTheClockRanAhead_IsTreatedAsTheClock(t *testing.T) {
	f := retentionFixture(t, 2)
	trueNow := historyTestTime
	year := trueNow.AddDate(1, 0, 0)
	tick := year
	f.s.SetClock(func() time.Time { tick = tick.Add(time.Millisecond); return tick })
	f.bump(t, 4) // trims with the clock a year ahead
	uid := f.rootUID(t)
	require.Greater(t, f.cutoffOf(t, uid), trueNow.Add(time.Hour).Format(skewTestLayout), "the stored cutoff is ahead")
	f.s.SetClock(func() time.Time { return trueNow })
	f.s.SetHistoryExportSource(func() bool { return true })
	data, err := f.s.Export(context.Background(), "PROJ", "0.6.0")
	require.NoError(t, err)
	e := entry("01U0000000000000000000Q001", uid, "status")
	e["ts"] = trueNow.Add(time.Minute).Format(skewTestLayout)
	file := editFile(t, data, func(m map[string]any) {
		for _, n := range m["nodes"].([]any) {
			if n.(map[string]any)["uid"] == uid {
				n.(map[string]any)["history"] = []any{e}
			}
		}
	})
	res := importMode(t, f.s, file, "merge")
	assert.Equal(t, 1, res.HistoryAdded, "the peer entry just after the true now is stored")
	assert.Equal(t, []string{"01U0000000000000000000Q001"}, historyIDs(t, f.db))
}

// The size report counts cutoffs later than the clock.
func TestHistorySizeReport_CutoffsLaterThanTheClock_AreCounted(t *testing.T) {
	f := retentionFixture(t, 2)
	f.bump(t, 4)
	uid := f.rootUID(t)
	rep, err := f.s.HistorySize(context.Background(), 3, 2)
	require.NoError(t, err)
	assert.Zero(t, rep.CutoffsAhead)
	_, err = f.db.Exec(`UPDATE history_retention_cutoff SET cutoff_ts = ? WHERE task_uid = ?`, historyTestTime.Add(time.Hour).Format(skewTestLayout), uid)
	require.NoError(t, err)
	rep, err = f.s.HistorySize(context.Background(), 3, 2)
	require.NoError(t, err)
	assert.Equal(t, 1, rep.CutoffsAhead)
}

// An exempt kind dated ahead, and an entry exactly at the clock, are not ahead.
func TestHistorySizeReport_AheadCountsOnlyChangeEntriesAfterTheClock(t *testing.T) {
	f := retentionFixture(t, 0)
	f.s.SetClock(func() time.Time { return historyTestTime })
	uid := f.rootUID(t)
	add := func(id, kind string, at time.Time) {
		_, err := f.db.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field)
			VALUES (?, ?, ?, 'tester', 'local', ?, 'x')`, id, uid, at.Format(skewTestLayout), kind)
		require.NoError(t, err)
	}
	add("01U0000000000000000000R001", "redaction", historyTestTime.Add(time.Hour))
	add("01U0000000000000000000R002", "change", historyTestTime)
	add("01U0000000000000000000R003", "change", historyTestTime.Add(time.Nanosecond))
	rep, err := f.s.HistorySize(context.Background(), 3, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, rep.Ahead, "only the change entry after the clock")
}
