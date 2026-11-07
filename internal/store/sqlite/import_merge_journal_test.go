// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-112.17.16.16: an import merge into a store that syncs
// journals what it changes, so a later hub edit of the same field meets a
// real local event instead of silently replacing the imported value. Written
// red-first against main, where the merge wrote no journal row.
package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// markJournalPushed makes the store one that has synced: every journal row it holds
// is past pending, as after a push.
func markJournalPushed(t *testing.T, s *sqlite.Store) {
	t.Helper()
	_, err := s.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed'`)
	require.NoError(t, err)
	synced, err := s.HasSyncedWithHub(context.Background())
	require.NoError(t, err)
	require.True(t, synced)
}

func countJournal(t *testing.T, s *sqlite.Store, where string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM sync_events WHERE `+where, args...).Scan(&n))
	return n
}

// retitled returns the store's export with node id retitled, hash included.
func retitled(t *testing.T, s *sqlite.Store, id, title string) *sqlite.ExportData {
	t.Helper()
	data := exportOf(t, s)
	for i := range data.Nodes {
		if data.Nodes[i].ID == id {
			data.Nodes[i].Title = title
			data.Nodes[i].ContentHash = model.ComputeContentHash(title, data.Nodes[i].Description,
				data.Nodes[i].Prompt, data.Nodes[i].Acceptance, nil)
		}
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	return data
}

func hubTitleEvent(t *testing.T, nodeID, title string, lamport, wall int64) *model.SyncEvent {
	t.Helper()
	pl, err := model.EncodePayload(&model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(strconv.Quote(title))})
	require.NoError(t, err)
	return &model.SyncEvent{EventID: clock.MustNewEventID(), ProjectPrefix: "REC", NodeID: nodeID,
		OpType: model.OpUpdateField, Payload: pl, WallClockTS: wall, LamportClock: lamport,
		VectorClock: model.VectorClock{"bob": lamport}, AuthorID: "bob", AuthorMachineHash: "0123456789abcdef",
		SyncStatus: model.SyncStatusApplied}
}

func applyHub(t *testing.T, s *sqlite.Store, e *model.SyncEvent) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error { return sqlite.IdempotentApply(ctx, tx, e) }))
}

func titleOf(t *testing.T, s *sqlite.Store, id string) string {
	t.Helper()
	var v string
	require.NoError(t, s.WriteDB().QueryRow(`SELECT title FROM nodes WHERE id = ?`, id).Scan(&v))
	return v
}

func syncedStoreWithTask(t *testing.T) *sqlite.Store {
	t.Helper()
	s := newTestStore(t)
	createPlainTask(t, s, "REC-1", 1, "", sameIDTime)
	markJournalPushed(t, s)
	return s
}

// TestImportMerge_SyncedStore_JournalsChangeAndHubEditCannotReplaceIt: a
// merge that retitles a task on a store that syncs journals the title, so a
// hub edit made before the import, pulled afterwards, loses to it by LWW and
// is logged as a conflict, instead of replacing the imported title silently.
func TestImportMerge_SyncedStore_JournalsChangeAndHubEditCannotReplaceIt(t *testing.T) {
	s := syncedStoreWithTask(t)
	mergeFile(t, s, retitled(t, s, "REC-1", "Imported title"))
	require.Equal(t, 1, countJournal(t, s, `op_type = 'update_field' AND sync_status = 'pending' AND payload LIKE '%Imported title%'`),
		"the import's title change is a pending journal row")

	applyHub(t, s, hubTitleEvent(t, "REC-1", "Hub title", 2, 1000))

	require.Equal(t, "Imported title", titleOf(t, s, "REC-1"), "the older hub edit does not replace the import")
	var conflicts int
	require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM sync_conflicts`).Scan(&conflicts))
	require.Equal(t, 1, conflicts, "the lost hub edit is logged, not dropped silently")
}

// TestImportMerge_RepeatImport_EmitsNothing: a second merge of the same file
// changes nothing and journals nothing, and only the changed field was
// journaled the first time.
func TestImportMerge_RepeatImport_EmitsNothing(t *testing.T) {
	s := syncedStoreWithTask(t)
	data := retitled(t, s, "REC-1", "Imported title")
	mergeFile(t, s, data)
	first := countJournal(t, s, `sync_status = 'pending'`)
	require.Equal(t, 1, first, "one changed field, one event")

	mergeFile(t, s, data)

	require.Equal(t, first, countJournal(t, s, `sync_status = 'pending'`), "a repeat import emits nothing")
}

// TestImportMerge_UnsyncedStore_WritesNoJournal: a store that has not synced
// keeps the old behaviour, so a later backfill still finds an empty journal
// for what the import wrote.
func TestImportMerge_UnsyncedStore_WritesNoJournal(t *testing.T) {
	s := newTestStore(t)
	createPlainTask(t, s, "REC-1", 1, "", sameIDTime)
	before := countJournal(t, s, `1 = 1`)

	mergeFile(t, s, retitled(t, s, "REC-1", "Imported title"))

	require.Equal(t, before, countJournal(t, s, `1 = 1`))
	require.Equal(t, "Imported title", titleOf(t, s, "REC-1"))
}

// TestImportMerge_HeldTaskStatusDependencyAndComment_AreJournaled: a task the
// sync log already holds reaches the journal with its non-default status, its
// dependency and its annotation, and the merge adds no create (ruling 1).
func TestImportMerge_HeldTaskStatusDependencyAndComment_AreJournaled(t *testing.T) {
	s := syncedStoreWithTask(t)
	createPlainTask(t, s, "REC-2", 2, "", sameIDTime)
	markJournalPushed(t, s)
	createsBefore := countJournal(t, s, `op_type = 'create_node'`)
	other := newTestStore(t)
	createPlainTask(t, other, "REC-1", 1, "", sameIDTime)
	createPlainTask(t, other, "REC-2", 2, "", sameIDTime)
	data := exportOf(t, other)
	for i := range data.Nodes {
		if data.Nodes[i].ID == "REC-2" {
			data.Nodes[i].Status = string(model.StatusDone)
			data.Nodes[i].Annotations = []model.Annotation{{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Author: "bob", Text: "note", CreatedAt: sameIDTime}}
		}
	}
	data.Dependencies = append(data.Dependencies, struct {
		FromID    string `json:"from_id"`
		ToID      string `json:"to_id"`
		DepType   string `json:"dep_type"`
		CreatedAt string `json:"created_at"`
	}{"REC-2", "REC-1", "related", sameIDTime.Format(time.RFC3339)})
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	mergeTheirs(t, s, data)

	for _, op := range []string{"transition_status", "link_dep", "comment"} {
		require.Equal(t, 1, countJournal(t, s, `op_type = ? AND node_id = 'REC-2' AND sync_status = 'pending'`, op), op)
	}
	require.Equal(t, createsBefore, countJournal(t, s, `op_type = 'create_node'`), "the merge journals no create")
}

// TestImportMerge_QueueCap_RefusesWholeImportAndNamesTheFix: with the cap too
// small for the import's events, the merge refuses before writing anything,
// names sync.max_queue_size and the command that drains the queue, and the
// same import succeeds once the queue is drained.
func TestImportMerge_QueueCap_RefusesWholeImportAndNamesTheFix(t *testing.T) {
	s := syncedStoreWithTask(t)
	_, err := s.WriteDB().Exec(`UPDATE meta SET value = '1' WHERE key = 'sync.max_queue_size'`)
	require.NoError(t, err)
	data := retitled(t, s, "REC-1", "Imported title")
	data.Nodes[0].Description = "Imported description"
	data.Nodes[0].ContentHash = model.ComputeContentHash("Imported title", "Imported description", "", "", nil)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	_, _, err = s.ImportReconcile(context.Background(), data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})

	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrSyncQueueFull), err.Error())
	require.Contains(t, err.Error(), "sync.max_queue_size")
	require.Contains(t, err.Error(), "mtix sync push")
	require.Equal(t, "Shared task", titleOf(t, s, "REC-1"), "nothing was written")
	require.Zero(t, countJournal(t, s, `sync_status = 'pending'`))

	markJournalPushed(t, s) // the drain the message names: pending events leave the queue
	_, _, err = s.ImportReconcile(context.Background(), data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.Error(t, err, "two events still exceed a cap of one")
	_, err = s.WriteDB().Exec(`UPDATE meta SET value = '0' WHERE key = 'sync.max_queue_size'`)
	require.NoError(t, err)
	_, _, err = s.ImportReconcile(context.Background(), data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.NoError(t, err)
}

// TestImportMerge_LargeImport_JournalsEveryChangeOnce measures a large merge:
// MTIX_IMPORT_JOURNAL_NODES sets the node count (default 300); the test logs
// the node count and the time of the merge.
func TestImportMerge_LargeImport_JournalsEveryChangeOnce(t *testing.T) {
	n := 300
	if v := os.Getenv("MTIX_IMPORT_JOURNAL_NODES"); v != "" {
		parsed, err := strconv.Atoi(v)
		require.NoError(t, err)
		n = parsed
	}
	s := newTestStore(t)
	for i := 1; i <= n; i++ {
		createPlainTask(t, s, fmt.Sprintf("REC-%d", i), i, "", sameIDTime)
	}
	markJournalPushed(t, s)
	data := exportOf(t, s)
	for i := range data.Nodes {
		data.Nodes[i].Title = "Imported " + data.Nodes[i].ID
		data.Nodes[i].ContentHash = model.ComputeContentHash(data.Nodes[i].Title, "", "", "", nil)
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	start := time.Now()
	mergeFile(t, s, data)
	elapsed := time.Since(start)

	t.Logf("import merge of %d changed nodes journaled in %s", n, elapsed)
	require.Equal(t, n, countJournal(t, s, `op_type = 'update_field' AND sync_status = 'pending'`))
}
