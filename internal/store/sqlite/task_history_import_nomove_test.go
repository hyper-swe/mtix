// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// THE RULE (MTIX-95.31.19.3): an import never moves history across keys. An
// import records history only for a task whose uid is present and unchanged on
// both sides (field changes, origin import). A task whose uid is new, changed,
// minted or absent starts fresh, and the history of the task it replaced stays
// under its old key. Stranding history is acceptable; grafting it onto another
// task never is. Moves the store itself performs on one task with a known uid
// (RenumberSubtree, RenameTo, the hub-rejection renumber, BackfillUIDs) are not
// imports and keep their history.

// importSnapshot is the state an import is judged against: where every earlier
// entry sits, and the value each task (by history key) held for each structured
// field before the import.
type importSnapshot struct {
	keyOf map[string]string            // entry id -> key
	held  map[string]map[string]string // key -> field -> value as an Old value is encoded
}

// structuredOldFields are the fields whose entries carry an Old value, with the
// nodes column each is read from.
var structuredOldFields = map[string]string{
	"status": "status", "assignee": "assignee", "priority": "priority", "labels": "labels",
	"node_type": "node_type", "issue_type": "issue_type", "parent": "parent_id",
	"defer_until": "defer_until", "deleted_at": "deleted_at", "agent_state": "agent_state",
	"estimate_min": "estimate_min", "actual_min": "actual_min", "weight": "weight", "display_path": "id",
}

// encodeOld mirrors how the recorder encodes a structured value: every empty
// value is the one canonical null.
func encodeOld(v sql.NullString) string {
	if !v.Valid || v.String == "" || v.String == "[]" {
		return "null"
	}
	b, _ := json.Marshal(v.String)
	return string(b)
}

// eachRow runs query and calls scan for every row, closing the cursor.
func eachRow(t *testing.T, db *sql.DB, query string, scan func(rows *sql.Rows)) {
	t.Helper()
	rows, err := db.Query(query)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	for rows.Next() {
		scan(rows)
	}
	require.NoError(t, rows.Err())
}

func snapImport(t *testing.T, db *sql.DB) importSnapshot {
	t.Helper()
	snap := importSnapshot{keyOf: map[string]string{}, held: map[string]map[string]string{}}
	eachRow(t, db, `SELECT entry_id, task_uid FROM task_history`, func(rows *sql.Rows) {
		var e, k string
		require.NoError(t, rows.Scan(&e, &k))
		snap.keyOf[e] = k
	})
	for field, col := range structuredOldFields {
		// A fixed column list from structuredOldFields; each task read by the key its history uses.
		eachRow(t, db, `SELECT COALESCE(NULLIF(uid, ''), id), `+col+` FROM nodes`, func(rows *sql.Rows) {
			var key string
			var v sql.NullString
			require.NoError(t, rows.Scan(&key, &v))
			if snap.held[key] == nil {
				snap.held[key] = map[string]string{}
			}
			snap.held[key][field] = encodeOld(v)
		})
	}
	return snap
}

// assertImportMovedNothing judges the history after an import against the
// snapshot taken before it: every earlier entry is still there, under the same
// key (nothing moved, nothing deleted); every new entry sits under a key some
// task holds; and every Old value an entry carries is the value that very key's
// task held before the import (a task new to the store holds none), so no value
// of one task is written onto another, whether or not the key had history.
func assertImportMovedNothing(t *testing.T, db *sql.DB, before importSnapshot) {
	t.Helper()
	live := map[string]bool{}
	eachRow(t, db, `SELECT COALESCE(NULLIF(uid, ''), id) FROM nodes`, func(rows *sql.Rows) {
		var k string
		require.NoError(t, rows.Scan(&k))
		live[k] = true
	})

	rows, err := db.Query(`SELECT entry_id, task_uid, field, old_value FROM task_history`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	seen := map[string]bool{}
	for rows.Next() {
		var entry, key, field string
		var oldV sql.NullString
		require.NoError(t, rows.Scan(&entry, &key, &field, &oldV))
		seen[entry] = true
		if was, ok := before.keyOf[entry]; ok {
			assert.Equal(t, was, key, "an earlier entry (%s) moved from %s to %s", field, was, key)
			continue
		}
		assert.True(t, live[key], "a new entry (%s) sits under %s, which no task holds", field, key)
		if !oldV.Valid {
			continue
		}
		want, held := before.held[key][field]
		assert.True(t, held, "a new entry (%s) carries an Old value under %s, a task that held none", field, key)
		assert.Equal(t, want, oldV.String, "the Old value of %s under %s is not that task's own earlier value", field, key)
	}
	require.NoError(t, rows.Err())
	for entry, key := range before.keyOf {
		assert.True(t, seen[entry], "an earlier entry of %s was deleted", key)
	}
}

type nomoveCase struct {
	name string
	run  func(t *testing.T) (local *sqlite.Store, file *sqlite.ExportData, mode sqlite.ImportMode, reconcile bool)
}

func nomoveCases() []nomoveCase {
	ctx := context.Background()
	two := func(t *testing.T, localUID, mateUID1, mateUID2 string, localMode func(*sqlite.Store)) (*sqlite.Store, *sqlite.ExportData) {
		local, mate := newTestStore(t), newTestStore(t)
		createSameIDTask(t, local, "REC-1", "", 1, localUID, "Write tests")
		prio := model.PriorityHigh // a non-workflow field, so a merge applies instead of refusing
		restore := ownedBy(t, "own-A")
		require.NoError(t, local.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Priority: &prio}))
		restore()
		createSameIDTask(t, mate, "REC-1", "", 1, mateUID1, "Write tests")
		if mateUID2 != "" {
			createSameIDTask(t, mate, "REC-2", "", 2, mateUID2, "Write tests")
		}
		if localMode != nil {
			localMode(local)
		}
		return local, exportOf(t, mate)
	}
	return []nomoveCase{
		{"X1 backfilled uids: B moves onto A's id, C is new", func(t *testing.T) (*sqlite.Store, *sqlite.ExportData, sqlite.ImportMode, bool) {
			uA, uB, uC := backfilledUID(t), backfilledUID(t), backfilledUID(t)
			local, mate := newTestStore(t), newTestStore(t)
			createSameIDTask(t, local, "REC-1", "", 1, uA, "Write tests")
			createSameIDTask(t, local, "REC-2", "", 2, uB, "Write tests")
			createSameIDTask(t, mate, "REC-1", "", 1, uB, "Write tests")
			createSameIDTask(t, mate, "REC-2", "", 2, uC, "Write tests")
			restore := ownedBy(t, "own-A")
			who := "alice-secret"
			require.NoError(t, local.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Assignee: &who}))
			restore()
			return local, exportOf(t, mate), sqlite.ImportModeReplace, false
		}},
		{"Y1b uid-less file: a stamped, moved local task and a different file task at its old id", func(t *testing.T) (*sqlite.Store, *sqlite.ExportData, sqlite.ImportMode, bool) {
			local, mate := newTestStore(t), newTestStore(t)
			createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local task")
			who := "carol-secret"
			require.NoError(t, local.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Assignee: &who}))
			legacyHistory(t, local.WriteDB())
			createSameIDTask(t, mate, "REC-1", "", 1, taskUID(t), "Teammate task")
			file := asSchemaV1(t, exportOf(t, mate))
			file.Nodes[0].UID = ""
			require.NoError(t, sqlite.RecomputeExportChecksum(file))
			return local, file, sqlite.ImportModeMerge, true
		}},
		{"Y1c stamp then in-place adoption", func(t *testing.T) (*sqlite.Store, *sqlite.ExportData, sqlite.ImportMode, bool) {
			local, mate := newTestStore(t), newTestStore(t)
			createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local task")
			prio := model.PriorityHigh
			require.NoError(t, local.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Priority: &prio}))
			legacyHistory(t, local.WriteDB())
			createSameIDTask(t, mate, "REC-1", "", 1, taskUID(t), "Teammate task")
			return local, exportOf(t, mate), sqlite.ImportModeMerge, true
		}},
		{"Y1d a different file task at the stamped, moved task's old id", func(t *testing.T) (*sqlite.Store, *sqlite.ExportData, sqlite.ImportMode, bool) {
			local, mate := newTestStore(t), newTestStore(t)
			createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local task")
			prio := model.PriorityHigh
			require.NoError(t, local.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Priority: &prio}))
			legacyHistory(t, local.WriteDB())
			createSameIDTask(t, mate, "REC-1", "", 1, backfilledUID(t), "Teammate task")
			file := exportOf(t, mate)
			file.Nodes[0].CreatedAt = "2026-09-01T00:00:00Z"
			require.NoError(t, sqlite.RecomputeExportChecksum(file))
			return local, file, sqlite.ImportModeMerge, true
		}},
		{"Y2 replace: C is identity-same to A at REC-1 and A itself is at REC-2", func(t *testing.T) (*sqlite.Store, *sqlite.ExportData, sqlite.ImportMode, bool) {
			uA, uC := backfilledUID(t), backfilledUID(t)
			local, file := two(t, uA, uC, uA, nil)
			return local, file, sqlite.ImportModeReplace, false
		}},
		{"Y3b plain merge, the Y2 shape", func(t *testing.T) (*sqlite.Store, *sqlite.ExportData, sqlite.ImportMode, bool) {
			uA, uC := backfilledUID(t), backfilledUID(t)
			local, file := two(t, uA, uC, uA, nil)
			return local, file, sqlite.ImportModeMerge, false
		}},
		{"Y4 reconcile merge, the Y2 shape", func(t *testing.T) (*sqlite.Store, *sqlite.ExportData, sqlite.ImportMode, bool) {
			uA, uC := backfilledUID(t), backfilledUID(t)
			local, file := two(t, uA, uC, uA, nil)
			return local, file, sqlite.ImportModeMerge, true
		}},
		{"adoption of a backfilled uid", func(t *testing.T) (*sqlite.Store, *sqlite.ExportData, sqlite.ImportMode, bool) {
			local, file := two(t, backfilledUID(t), taskUID(t), "", nil)
			return local, file, sqlite.ImportModeMerge, false
		}},
		{"replace over a same-title task with a create-time uid (X5)", func(t *testing.T) (*sqlite.Store, *sqlite.ExportData, sqlite.ImportMode, bool) {
			local, file := two(t, taskUID(t), taskUID(t), "", nil)
			return local, file, sqlite.ImportModeReplace, false
		}},
		{"replace retitles the task under a backfilled uid", func(t *testing.T) (*sqlite.Store, *sqlite.ExportData, sqlite.ImportMode, bool) {
			local, file := two(t, backfilledUID(t), backfilledUID(t), "", nil)
			file.Nodes[0].Title = "Renamed in the file"
			require.NoError(t, sqlite.RecomputeExportChecksum(file))
			return local, file, sqlite.ImportModeReplace, false
		}},
		{"replace over a uid-less local task", func(t *testing.T) (*sqlite.Store, *sqlite.ExportData, sqlite.ImportMode, bool) {
			local, file := two(t, taskUID(t), taskUID(t), "", func(l *sqlite.Store) { legacyHistory(t, l.WriteDB()) })
			return local, file, sqlite.ImportModeReplace, false
		}},
	}
}

// TestTaskHistory_Import_NeverMovesHistoryAcrossKeys runs every grafting
// shape the reviews found (X1 with backfilled uids, Y1b, Y1c, Y1d, Y2 and the
// others) and asserts the rule: nothing moves, and no task new to the store is
// given another task's values.
func TestTaskHistory_Import_NeverMovesHistoryAcrossKeys(t *testing.T) {
	for _, tc := range nomoveCases() {
		t.Run(tc.name, func(t *testing.T) {
			local, file, mode, reconcile := tc.run(t)
			db := local.WriteDB()
			before := snapImport(t, db)
			t.Setenv("MTIX_AUTHOR_ID", "importer")
			var err error
			if reconcile {
				_, _, err = local.ImportReconcile(context.Background(), file, sqlite.ImportReconcileOptions{Mode: mode, Confirm: true, Force: true})
			} else {
				_, err = local.Import(context.Background(), file, mode, true)
			}
			require.NoError(t, err, "every case in this table applies; a refusal fails it")
			assertImportMovedNothing(t, db, before)
		})
	}
}

// TestTaskHistory_Import_UIDlessTaskUnchanged_RecordsNothing: an import records
// no task that has no uid (there is no identity to pair it by), so a legacy
// board is not given a fresh creation record on every import.
func TestTaskHistory_Import_UIDlessTaskUnchanged_RecordsNothing(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Legacy task")
	legacyHistory(t, local.WriteDB())
	file := asSchemaV1(t, exportOf(t, local))
	file.Nodes[0].UID = ""
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	db := local.WriteDB()
	before := historyCount(t, db, ``)

	_, err := local.Import(ctx, file, sqlite.ImportModeMerge, true)
	require.NoError(t, err)

	assert.Equal(t, before, historyCount(t, db, ``))
}

// TestTaskHistory_Import_MergeChangesAUIDlessTaskThatStaysUIDless_RecordsNothing:
// a merge that changes the content of a task which has no uid before or after
// records nothing for it (no identity to pair by); pairing it by its id would
// write its change under the id key.
func TestTaskHistory_Import_MergeChangesAUIDlessTaskThatStaysUIDless_RecordsNothing(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Legacy task")
	legacyHistory(t, local.WriteDB())
	file := asSchemaV1(t, exportOf(t, local))
	file.Nodes[0].UID = ""
	file.Nodes[0].Description, file.Nodes[0].ContentHash = "changed in the file", "changed-hash"
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	db := local.WriteDB()
	before := historyCount(t, db, ``)

	_, err := local.Import(ctx, file, sqlite.ImportModeMerge, true)
	require.NoError(t, err)

	var desc string
	require.NoError(t, db.QueryRow(`SELECT COALESCE(description, '') FROM nodes WHERE id = 'REC-1'`).Scan(&desc))
	require.Equal(t, "changed in the file", desc, "the merge applied the change")
	assert.Equal(t, before, historyCount(t, db, ``), "and recorded nothing for the uid-less task")
}
