// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// unionFile exports the golden board with the given history rows, as a file
// read back from disk (history.export on, MTIX-95.31.19.5).
func unionFile(t *testing.T, rows ...[3]string) (*sqlite.ExportData, string) {
	t.Helper()
	src, raw := goldenStore(t)
	uid := goldenRootUID(t, raw)
	for _, r := range rows { // entry id, uid ("" = the golden root), field
		u := r[1]
		if u == "" {
			u = uid
		}
		insertHistory(t, src.db, r[0], u, r[2])
	}
	src.s.SetHistoryExportSource(func() bool { return true })
	exp, err := src.s.Export(context.Background(), "PROJ", "0.6.0")
	require.NoError(t, err)
	var file sqlite.ExportData
	require.NoError(t, json.Unmarshal(marshalExport(t, exp), &file))
	return &file, uid
}

// unionDest returns a store holding the golden board and the given local rows.
func unionDest(t *testing.T, rows ...[3]string) (*historyFixture, string) {
	t.Helper()
	dst, raw := goldenStore(t)
	uid := goldenRootUID(t, raw)
	for _, r := range rows {
		u := r[1]
		if u == "" {
			u = uid
		}
		insertHistory(t, dst.db, r[0], u, r[2])
	}
	return dst, uid
}

func historyIDs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT entry_id FROM task_history WHERE entry_id LIKE '01U%' ORDER BY entry_id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

func importMode(t *testing.T, s *sqlite.Store, file *sqlite.ExportData, mode sqlite.ImportMode) *sqlite.ImportResult {
	t.Helper()
	res, err := s.Import(context.Background(), file, mode, true)
	require.NoError(t, err)
	return res
}

var bothModes = []sqlite.ImportMode{sqlite.ImportModeReplace, sqlite.ImportModeMerge}

func TestImportHistory_UnionsLocalAndFileEntries_InBothModes(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(string(mode), func(t *testing.T) {
			file, _ := unionFile(t, [3]string{"01U0000000000000000000F001", "", "status"},
				[3]string{"01U0000000000000000000F002", "ghost-uid", "priority"})
			dst, _ := unionDest(t, [3]string{"01U0000000000000000000L001", "", "title"})
			res := importMode(t, dst.s, file, mode)
			assert.Equal(t, []string{"01U0000000000000000000F001", "01U0000000000000000000F002", "01U0000000000000000000L001"},
				historyIDs(t, dst.db), "the local entry stays and the file's are added")
			assert.Equal(t, 2, res.HistoryAdded)
			assert.Zero(t, res.HistoryConflicts)
			assert.Zero(t, res.HistorySkippedLegacyKey)
		})
	}
}

func TestImportHistory_SecondRun_AddsNothingAndCountsPresent(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(string(mode), func(t *testing.T) {
			file, _ := unionFile(t, [3]string{"01U0000000000000000000F001", "", "status"},
				[3]string{"01U0000000000000000000F002", "ghost-uid", "priority"})
			dst, _ := unionDest(t)
			importMode(t, dst.s, file, mode)
			res := importMode(t, dst.s, file, mode)
			assert.Zero(t, res.HistoryAdded)
			assert.Equal(t, 2, res.HistorySkipped)
			assert.Len(t, historyIDs(t, dst.db), 2)
		})
	}
}

func TestImportHistory_ShuffledDuplicatedEntries_GiveTheSameStoreInAnyOrder(t *testing.T) {
	base, uid := unionFile(t)
	var entries []map[string]any
	for i := 0; i < 12; i++ {
		entries = append(entries, map[string]any{"id": fmt.Sprintf("01U00000000000000000P%05d", i), "task_uid": uid, "ts": "2026-03-14T12:00:00.000000000Z",
			"actor": "t", "origin": "local", "kind": "change", "field": fmt.Sprintf("f%d", i)})
	}
	var want []string
	for seed := int64(1); seed <= 6; seed++ {
		for _, mode := range bothModes {
			rng := rand.New(rand.NewSource(seed))
			picked := append([]map[string]any{}, entries...)
			picked = append(picked, entries[:4]...) // duplicates
			rng.Shuffle(len(picked), func(i, j int) { picked[i], picked[j] = picked[j], picked[i] })
			file := editFile(t, base, func(m map[string]any) { node0(m)["history"] = picked })
			dst, _ := unionDest(t)
			importMode(t, dst.s, file, mode)
			got := historyIDs(t, dst.db)
			if want == nil {
				want = got
			}
			assert.Equal(t, want, got, "seed %d %s", seed, mode)
			assert.Len(t, got, 12)
		}
	}
}

// editFile applies fn to the file's JSON form and returns the result with its
// checksum recomputed; entries are plain maps, as a file states them.
func editFile(t *testing.T, base *sqlite.ExportData, fn func(m map[string]any)) *sqlite.ExportData {
	t.Helper()
	raw, err := json.Marshal(base)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	fn(m)
	raw, err = json.Marshal(m)
	require.NoError(t, err)
	var out sqlite.ExportData
	require.NoError(t, json.Unmarshal(raw, &out))
	require.NoError(t, sqlite.RecomputeExportChecksum(&out))
	return &out
}

func entry(id, uid, field string) map[string]any {
	return map[string]any{"id": id, "task_uid": uid, "ts": "2026-03-14T12:00:00.000000000Z",
		"actor": "tester", "origin": "local", "kind": "change", "field": field}
}

func node0(m map[string]any) map[string]any { return m["nodes"].([]any)[0].(map[string]any) }

func TestImportHistory_LegacyKeyEntries_SkippedAndCountedNeverStored(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(string(mode), func(t *testing.T) {
			base, _ := unionFile(t, [3]string{"01U0000000000000000000G001", "", "status"})
			file := editFile(t, base, func(m map[string]any) {
				m["history"] = []any{entry("01U0000000000000000000G002", "PROJ-1", "status"),
					entry("01U0000000000000000000G003", "PROJ-9.2.1", "status")}
			})
			dst, _ := unionDest(t)
			res := importMode(t, dst.s, file, mode)
			assert.Equal(t, []string{"01U0000000000000000000G001"}, historyIDs(t, dst.db))
			assert.Equal(t, 2, res.HistorySkippedLegacyKey)
			assert.Equal(t, 1, res.HistoryAdded)
			require.NotEmpty(t, res.Warnings)
		})
	}
}

func TestImportHistory_UIDlessNodeEntry_LegacyKeySkipped(t *testing.T) {
	base, _ := unionFile(t)
	file := editFile(t, base, func(m map[string]any) {
		n := node0(m)
		delete(n, "uid")
		n["history"] = []any{entry("01U0000000000000000000H001", n["id"].(string), "status")}
	})
	dst, _ := unionDest(t)
	res := importMode(t, dst.s, file, sqlite.ImportModeReplace)
	assert.Empty(t, historyIDs(t, dst.db))
	assert.Equal(t, 1, res.HistorySkippedLegacyKey)
}

func rowOf(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var uid, ts, actor, origin, kind, field string
	var oldV, newV, ev sql.NullString
	require.NoError(t, db.QueryRow(`SELECT task_uid, ts, actor, origin, kind, field, old_value, new_value, event_id
		FROM task_history WHERE entry_id = ?`, id).Scan(&uid, &ts, &actor, &origin, &kind, &field, &oldV, &newV, &ev))
	return fmt.Sprint(uid, ts, actor, origin, kind, field, oldV, newV, ev)
}

func TestImportHistory_SameEntryIDDifferentContent_KeepsLocalRowAndCountsConflict(t *testing.T) {
	cases := map[string]func(e map[string]any){
		"actor":    func(e map[string]any) { e["actor"] = "mallory" },
		"field":    func(e map[string]any) { e["field"] = "priority" },
		"task_uid": func(e map[string]any) { e["task_uid"] = "ghost-uid" },
	}
	for name, mutate := range cases {
		for _, mode := range bothModes {
			t.Run(name+"/"+string(mode), func(t *testing.T) {
				base, uid := unionFile(t)
				e := entry("01U0000000000000000000I001", uid, "status")
				mutate(e)
				file := editFile(t, base, func(m map[string]any) {
					if name == "task_uid" { // another task: the top-level list
						m["history"] = []any{e}
					} else {
						node0(m)["history"] = []any{e}
					}
				})
				dst, _ := unionDest(t, [3]string{"01U0000000000000000000I001", "", "status"})
				before := rowOf(t, dst.db, "01U0000000000000000000I001")
				res := importMode(t, dst.s, file, mode)
				assert.Equal(t, before, rowOf(t, dst.db, "01U0000000000000000000I001"), "the local row is byte-identical")
				assert.Equal(t, []string{"01U0000000000000000000I001"}, historyIDs(t, dst.db))
				assert.Equal(t, 1, res.HistoryConflicts)
				assert.Zero(t, res.HistoryAdded)
				require.NotEmpty(t, res.Warnings)
				joined := strings.Join(res.Warnings, "\n")
				assert.Contains(t, joined, "01U0000000000000000000I001")
				assert.NotContains(t, joined, "mallory", "a warning carries no content")
			})
		}
	}
}

func TestImportHistory_EventKeyCollision_TreatedAsPresent(t *testing.T) {
	base, uid := unionFile(t)
	e := entry("01U0000000000000000000J002", uid, "status")
	e["event_id"] = "ev-1"
	file := editFile(t, base, func(m map[string]any) { node0(m)["history"] = []any{e} })
	dst, _ := unionDest(t)
	_, err := dst.db.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, event_id)
		VALUES ('01U0000000000000000000J001', ?, '2026-03-14T12:00:00.000000000Z', 'x', 'local', 'change', 'status', 'ev-1')`, uid)
	require.NoError(t, err)
	for _, mode := range bothModes {
		res := importMode(t, dst.s, file, mode)
		assert.Zero(t, res.HistoryAdded, string(mode))
		assert.Equal(t, 1, res.HistorySkipped, string(mode))
		assert.Equal(t, []string{"01U0000000000000000000J001"}, historyIDs(t, dst.db))
	}
}

func TestImportHistory_FileWithoutHistory_KeepsEveryLocalEntry(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(string(mode), func(t *testing.T) {
			_, raw := goldenStore(t)
			var plain sqlite.ExportData
			require.NoError(t, json.Unmarshal(raw, &plain)) // the 2.0.0 golden file: no history
			dst, _ := unionDest(t, [3]string{"01U0000000000000000000K001", "", "status"},
				[3]string{"01U0000000000000000000K002", "ghost-uid", "status"})
			res := importMode(t, dst.s, &plain, mode)
			assert.Equal(t, []string{"01U0000000000000000000K001", "01U0000000000000000000K002"}, historyIDs(t, dst.db))
			assert.Zero(t, res.HistoryAdded+res.HistorySkipped+res.HistoryConflicts+res.HistorySkippedLegacyKey)
		})
	}
}

func TestImportHistory_ConcurrentMergeImports_EndWithOneCopyOfEachEntry(t *testing.T) {
	file, _ := unionFile(t, [3]string{"01U0000000000000000000M001", "", "status"},
		[3]string{"01U0000000000000000000M002", "ghost-uid", "priority"})
	dst, _ := unionDest(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := dst.s.Import(context.Background(), file, sqlite.ImportModeMerge, true)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err) // MTIX-95.31.27: no import may fail
	}
	ids := historyIDs(t, dst.db)
	sort.Strings(ids)
	assert.Equal(t, []string{"01U0000000000000000000M001", "01U0000000000000000000M002"}, ids)
}

func TestImportHistory_InFileDuplicateIDWithDifferentContent_SkippedWhateverTheOrder(t *testing.T) {
	for _, mode := range bothModes {
		base, uid := unionFile(t)
		a := entry("01U0000000000000000000N001", uid, "status")
		b := entry("01U0000000000000000000N001", uid, "status")
		b["actor"] = "mallory"
		ok := entry("01U0000000000000000000N002", uid, "priority")
		var want []string
		for i, order := range [][]any{{a, b, ok}, {b, a, ok}, {ok, b, a, a}} {
			file := editFile(t, base, func(m map[string]any) {
				node0(m)["history"] = order
			})
			dst, _ := unionDest(t)
			res := importMode(t, dst.s, file, mode)
			got := historyIDs(t, dst.db)
			if i == 0 {
				want = got
			}
			assert.Equal(t, want, got, "%s order %d", mode, i)
			assert.Equal(t, []string{"01U0000000000000000000N002"}, got, "neither differing copy is stored")
			assert.Equal(t, 1, res.HistoryConflicts, "%s order %d", mode, i)
			require.NotEmpty(t, res.Warnings)
			assert.Contains(t, strings.Join(res.Warnings, "\n"), "within the import file")
			assert.NotContains(t, strings.Join(res.Warnings, "\n"), "mallory")
		}
	}
}

func TestImportHistory_ConflictDetection_EveryFieldCounts(t *testing.T) {
	fields := map[string]func(e map[string]any){
		"ts":        func(e map[string]any) { e["ts"] = "2026-03-14T12:00:01.000000000Z" },
		"origin":    func(e map[string]any) { e["origin"] = "hub" },
		"kind":      func(e map[string]any) { e["kind"] = "redaction" },
		"event_id":  func(e map[string]any) { e["event_id"] = "ev-other" },
		"old unset": func(e map[string]any) { delete(e, "old") },
		"old other": func(e map[string]any) { e["old"] = `"closed"` },
		"new other": func(e map[string]any) { e["new"] = `"closed"` },
		"new unset": func(e map[string]any) { delete(e, "new") },
	}
	for name, mutate := range fields {
		for _, mode := range bothModes {
			t.Run(name+"/"+string(mode), func(t *testing.T) {
				base, uid := unionFile(t)
				e := entry("01U0000000000000000000O001", uid, "status")
				e["old"], e["new"] = `"open"`, `"done"`
				file := editFile(t, base, func(m map[string]any) {
					mutate(e)
					node0(m)["history"] = []any{e}
				})
				dst, _ := unionDest(t)
				_, err := dst.db.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, old_value, new_value)
					VALUES ('01U0000000000000000000O001', ?, '2026-03-14T12:00:00.000000000Z', 'tester', 'local', 'change', 'status', '"open"', '"done"')`, uid)
				require.NoError(t, err)
				before := rowOf(t, dst.db, "01U0000000000000000000O001")
				res := importMode(t, dst.s, file, mode)
				assert.Equal(t, 1, res.HistoryConflicts)
				assert.Zero(t, res.HistorySkipped)
				assert.Equal(t, before, rowOf(t, dst.db, "01U0000000000000000000O001"))
			})
		}
	}
}

// Entries with different ids that share an event key (task_uid, event_id,
// field) are one event: identical content collapses to the smallest id, and
// differing content is skipped whole, whatever the order.
func TestImportHistory_InFileEventKeyCollision_OrderIndependent(t *testing.T) {
	for _, mode := range bothModes {
		base, uid := unionFile(t)
		mk := func(id, actor string) map[string]any {
			e := entry(id, uid, "status")
			e["event_id"], e["actor"] = "ev-9", actor
			return e
		}
		other := entry("01U0000000000000000000R009", uid, "priority")
		cases := map[string]struct {
			entries   []any
			wantIDs   []string
			conflicts int
			skipped   int
		}{
			"differing": {[]any{mk("01U0000000000000000000R001", "a"), mk("01U0000000000000000000R002", "b"), other},
				[]string{"01U0000000000000000000R009"}, 2, 0},
			"identical": {[]any{mk("01U0000000000000000000R001", "a"), mk("01U0000000000000000000R002", "a"), other},
				[]string{"01U0000000000000000000R001", "01U0000000000000000000R009"}, 0, 1},
		}
		for name, c := range cases {
			for _, order := range [][]int{{0, 1, 2}, {1, 0, 2}, {2, 1, 0}} {
				var shuffled []any
				for _, i := range order {
					shuffled = append(shuffled, c.entries[i])
				}
				file := editFile(t, base, func(m map[string]any) { node0(m)["history"] = shuffled })
				dst, _ := unionDest(t)
				res := importMode(t, dst.s, file, mode)
				assert.Equal(t, c.wantIDs, historyIDs(t, dst.db), "%s %s %v", name, mode, order)
				assert.Equal(t, c.conflicts, res.HistoryConflicts, "%s %s %v", name, mode, order)
				assert.Equal(t, c.skipped, res.HistorySkipped, "%s %s %v", name, mode, order)
			}
		}
	}
}

// An import that applied but failed its index rebuild is still reported as
// applied, with the same bookkeeping as a complete one.
func TestImportReconcile_AppliedButIncomplete_ReportIsMarkedApplied(t *testing.T) {
	file, _ := unionFile(t)
	dst, _ := unionDest(t)
	_, err := dst.db.Exec(`CREATE TRIGGER test_fail_fts_rebuild BEFORE DELETE ON nodes_fts_data
		BEGIN SELECT RAISE(ABORT, 'test: fts rebuild fails'); END`)
	require.NoError(t, err)
	report, res, err := dst.s.ImportReconcile(context.Background(), file, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Force: true, Confirm: true})
	require.ErrorIs(t, err, sqlite.ErrImportIncomplete)
	require.NotNil(t, res)
	assert.True(t, report.Applied)
	assert.Equal(t, len(file.Nodes), report.Idempotent, "every node was an exact no-op, as on a complete import")
	assert.Equal(t, res.WorkflowResolved, report.WorkflowConflicts)
}
