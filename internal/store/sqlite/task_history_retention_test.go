// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// tickingClock returns a clock that advances one millisecond per call, so
// entries written in sequence sort by time and by ULID alike.
func tickingClock() func() time.Time {
	t := historyTestTime
	return func() time.Time { t = t.Add(time.Millisecond); return t }
}

// retentionFixture is a seeded store whose root task changes priority on
// demand, one history entry per change, with the given limit (0 = unset).
func retentionFixture(t *testing.T, limit int) *historyFixture {
	t.Helper()
	f := newHistoryFixture(t)
	f.seed(t)
	f.s.SetClock(tickingClock())
	f.s.SetHistoryLimitSource(func() int { return limit })
	return f
}

func (f *historyFixture) bump(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		p := model.Priority(i%5 + 1)
		cur, err := f.s.GetNode(context.Background(), ccRoot)
		require.NoError(t, err)
		if cur.Priority == p {
			p = model.Priority(p%5 + 1)
		}
		require.NoError(t, f.s.UpdateNode(context.Background(), ccRoot, &store.NodeUpdate{Priority: &p}))
	}
}

func (f *historyFixture) count(t *testing.T, kind string) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE kind = ?`, kind).Scan(&n))
	return n
}

func TestRetention_LimitUnset_NeverTrims(t *testing.T) {
	f := retentionFixture(t, 0)
	f.bump(t, 40)
	assert.Equal(t, 40, f.count(t, "change"))
	assert.Zero(t, f.count(t, "retention_drop"))
}

// Trimming starts when the change entries exceed N and goes down to
// max(1, floor(0.9N)), with one marker per run.
func TestRetention_LimitSet_TrimsToFloorOfNinetyPercentWithOneMarker(t *testing.T) {
	tests := []struct{ n, wantKept, wantDropped int }{
		{1, 1, 1}, {2, 1, 2}, {10, 9, 2}, {1000, 900, 101},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint("N=", tt.n), func(t *testing.T) {
			f := retentionFixture(t, tt.n)
			f.bump(t, tt.n)
			assert.Equal(t, tt.n, f.count(t, "change"), "at the limit nothing is trimmed")
			assert.Zero(t, f.count(t, "retention_drop"))
			f.bump(t, 1)
			assert.Equal(t, tt.wantKept, f.count(t, "change"))
			assert.Equal(t, 1, f.count(t, "retention_drop"))
			var field, newV, oldV, origin string
			require.NoError(t, f.db.QueryRow(`SELECT field, new_value, old_value, origin FROM task_history WHERE kind = 'retention_drop'`).
				Scan(&field, &newV, &oldV, &origin))
			assert.Equal(t, "retention", field)
			assert.Equal(t, fmt.Sprint(tt.wantDropped), newV, "the count dropped, as a decimal string")
			assert.Equal(t, "system", origin)
			// The cutoff is the ts of the newest dropped entry: every kept entry is later.
			var kept int
			require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE kind = 'change' AND ts > ?`, oldV).Scan(&kept))
			assert.Equal(t, tt.wantKept, kept)
		})
	}
}

func TestRetention_ExemptRecords_AreNeverCountedOrTrimmed(t *testing.T) {
	f := retentionFixture(t, 10)
	uid := f.rootUID(t)
	for i, kind := range []string{"redaction", "retention_drop", "redaction", "retention_drop", "redaction"} {
		_, err := f.db.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, old_value, new_value)
			VALUES (?, ?, ?, 'a', 'system', ?, 'x', NULL, NULL)`,
			fmt.Sprintf("01E0000000000000000000000%d", i), uid, fmt.Sprintf("2020-01-01T00:00:00.00000000%dZ", i), kind)
		require.NoError(t, err)
	}
	f.bump(t, 10)
	assert.Equal(t, 10, f.count(t, "change"), "five exempt rows do not push the count over 10")
	f.bump(t, 1)
	assert.Equal(t, 9, f.count(t, "change"))
	assert.Equal(t, 3, f.count(t, "redaction"))
	assert.Equal(t, 3, f.count(t, "retention_drop"), "the two old markers and the new one")
}

func TestRetention_LoweredLimit_TrimsAtTheNextAppendOnly(t *testing.T) {
	limit := 0
	f := retentionFixture(t, 0)
	f.s.SetHistoryLimitSource(func() int { return limit })
	f.bump(t, 20)
	limit = 5
	assert.Equal(t, 20, f.count(t, "change"), "no sweep when the limit is lowered")
	f.bump(t, 1)
	assert.Equal(t, 4, f.count(t, "change"), "21 > 5: trimmed to floor(4.5)")
}

func TestRetention_SecondRun_WritesAnotherMarkerAndLeavesTheFirstUntouched(t *testing.T) {
	f := retentionFixture(t, 2)
	f.bump(t, 3)
	first := f.markerRows(t)
	require.Len(t, first, 1)
	f.bump(t, 3)
	all := f.markerRows(t)
	require.Len(t, all, 2)
	assert.Equal(t, first[0], all[0], "a marker is an immutable row")
}

func (f *historyFixture) markerRows(t *testing.T) []string {
	t.Helper()
	rows, err := f.db.Query(`SELECT entry_id||'|'||ts||'|'||COALESCE(old_value,'')||'|'||COALESCE(new_value,'') FROM task_history WHERE kind = 'retention_drop' ORDER BY ts, entry_id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	return out
}

func (f *historyFixture) rootUID(t *testing.T) string {
	t.Helper()
	var uid sql.NullString
	require.NoError(t, f.db.QueryRow(`SELECT uid FROM nodes WHERE id = ?`, ccRoot).Scan(&uid))
	if uid.String == "" {
		return ccRoot
	}
	return uid.String
}

func TestListTaskHistory_NewestFirst_LimitedAndValueFreeForFreeText(t *testing.T) {
	f := retentionFixture(t, 0)
	require.NoError(t, f.s.UpdateNode(context.Background(), ccRoot, &store.NodeUpdate{Title: strPtr("secret title")}))
	f.bump(t, 3)
	got, err := f.s.ListTaskHistory(context.Background(), ccRoot, 3)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.True(t, got[0].Timestamp.After(got[1].Timestamp) && got[1].Timestamp.After(got[2].Timestamp))
	all, err := f.s.ListTaskHistory(context.Background(), ccRoot, 50)
	require.NoError(t, err)
	last := all[len(all)-1]
	assert.Equal(t, "title", last.Field)
	assert.Nil(t, last.Old)
	assert.Nil(t, last.New)
	assert.NotEmpty(t, got[0].Actor)
	assert.NotEmpty(t, got[0].Origin)
	_, err = f.s.ListTaskHistory(context.Background(), "NOPE-9", 5)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestHistorySizeReport_CountsRowsLargestTasksAndOversize(t *testing.T) {
	f := retentionFixture(t, 0)
	f.bump(t, 6)
	rep, err := f.s.HistorySize(context.Background(), 3, 4)
	require.NoError(t, err)
	assert.Equal(t, 6, rep.Rows)
	require.NotEmpty(t, rep.Largest)
	assert.Equal(t, ccRoot, rep.Largest[0].ID)
	assert.Equal(t, 6, rep.Largest[0].Entries)
	assert.Equal(t, 1, rep.Oversize, "one task holds more change entries than the limit of 4")
}

// Entries written at one instant are trimmed in the order they were appended.
func TestRetention_EqualTimestamps_TrimInAppendOrder(t *testing.T) {
	f := newHistoryFixture(t) // the fixed clock: every entry shares one ts
	f.seed(t)
	f.s.SetHistoryLimitSource(func() int { return 2 })
	for _, p := range []model.Priority{2, 3, 4} {
		p := p
		require.NoError(t, f.s.UpdateNode(context.Background(), ccRoot, &store.NodeUpdate{Priority: &p}))
	}
	var kept string
	require.NoError(t, f.db.QueryRow(`SELECT new_value FROM task_history WHERE kind = 'change' AND field = 'priority'`).Scan(&kept))
	assert.Equal(t, `"4"`, kept, "the latest change survives, the earlier ones were dropped")
}

func (f *historyFixture) cutoffOf(t *testing.T, uid string) string {
	t.Helper()
	var c string
	require.NoError(t, f.db.QueryRow(`SELECT cutoff_ts FROM history_retention_cutoff WHERE task_uid = ?`, uid).Scan(&c))
	return c
}

// Entries dated far ahead (a clock far ahead, or a crafted file) are refused on
// import and counted, so the local changes that follow are kept.
func TestRetention_FutureDatedImportedEntries_AreRefusedAndLocalChangesSurvive(t *testing.T) {
	f := retentionFixture(t, 10)
	f.bump(t, 1)
	f.s.SetHistoryExportSource(func() bool { return true })
	data, err := f.s.Export(context.Background(), "PROJ", "0.6.0")
	require.NoError(t, err)
	uid := f.rootUID(t)
	var future []any
	for i := 0; i < 20; i++ {
		e := entry(fmt.Sprintf("01U0000000000000000000H%03d", i), uid, "status")
		e["ts"] = fmt.Sprintf("2999-01-01T00:00:%02d.000000000Z", i)
		future = append(future, e)
	}
	file := editFile(t, data, func(m map[string]any) {
		for _, n := range m["nodes"].([]any) {
			if n.(map[string]any)["uid"] == uid {
				n.(map[string]any)["history"] = future
			}
		}
	})
	res, err := f.s.Import(context.Background(), file, sqlite.ImportModeMerge, true)
	require.NoError(t, err)
	assert.Equal(t, 20, res.HistorySkipped)
	assert.Contains(t, fmt.Sprint(res.Warnings), "in the future")
	f.bump(t, 15) // over the limit: trims the oldest local entries
	var kept int
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE kind = 'change' AND ts < '2900'`).Scan(&kept))
	assert.GreaterOrEqual(t, kept, 9, "the local changes are kept, at the floor or the limit")
	assert.LessOrEqual(t, kept, 10)
	assert.Less(t, f.cutoffOf(t, uid), "2900", "the cutoff is never in the future")
}

// A future-dated row already in the store cannot push the cutoff past now.
func TestRetention_ExistingFutureDatedRows_CannotPushTheCutoffPastNow(t *testing.T) {
	f := retentionFixture(t, 10)
	uid := f.rootUID(t)
	for i := 0; i < 20; i++ {
		insertHistory(t, f.db, fmt.Sprintf("01U0000000000000000000I%03d", i), uid, "status")
		_, err := f.db.Exec(`UPDATE task_history SET ts = ? WHERE entry_id = ?`, fmt.Sprintf("2999-01-01T00:00:%02d.000000000Z", i), fmt.Sprintf("01U0000000000000000000I%03d", i))
		require.NoError(t, err)
	}
	f.bump(t, 15) // the local changes alone exceed the limit
	assert.Less(t, f.cutoffOf(t, uid), "2900")
}

// A later trim with an older cutoff never lowers the stored one.
func TestRetention_LaterTrimWithAnOlderCutoff_KeepsTheNewerCutoff(t *testing.T) {
	f := retentionFixture(t, 2)
	f.bump(t, 3)
	uid := f.rootUID(t)
	_, err := f.db.Exec(`UPDATE history_retention_cutoff SET cutoff_ts = '2026-12-31T00:00:00.000000000Z' WHERE task_uid = ?`, uid)
	require.NoError(t, err)
	f.bump(t, 3) // trims again, dropping entries older than the stored cutoff
	assert.Equal(t, "2026-12-31T00:00:00.000000000Z", f.cutoffOf(t, uid))
}
