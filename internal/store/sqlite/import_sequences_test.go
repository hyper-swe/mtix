// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// cloneExport returns an independent copy of data, so concurrent imports do
// not share the caller's node structs (an import normalises them in place).
func cloneExport(t *testing.T, data *sqlite.ExportData) *sqlite.ExportData {
	t.Helper()
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	var out sqlite.ExportData
	require.NoError(t, json.Unmarshal(raw, &out))
	return &out
}

// runConcurrentMergeImports runs n merge imports of data against dst at once
// and returns each import's error (MTIX-95.31.27).
func runConcurrentMergeImports(t *testing.T, dst *sqlite.Store, data *sqlite.ExportData, n int) []error {
	t.Helper()
	copies := make([]*sqlite.ExportData, n)
	for i := range copies {
		copies[i] = cloneExport(t, data)
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = dst.Import(context.Background(), copies[i], sqlite.ImportModeMerge, true)
		}(i)
	}
	wg.Wait()
	return errs
}

// TestImport_ConcurrentMergeImports_AllSucceed verifies that concurrent merge
// imports all succeed: the sequence rebuild must not collide on sequences.key
// (MTIX-95.31.27, FR-7.8).
func TestImport_ConcurrentMergeImports_AllSucceed(t *testing.T) {
	data := makeTestExport(t, newTestStore(t))
	dst := newTestStore(t)
	for i, err := range runConcurrentMergeImports(t, dst, data, 8) {
		require.NoError(t, err, "import %d", i)
	}
}

// TestImport_ConcurrentMergeImports_SequenceCoversMaxID verifies that after
// concurrent imports the counter is at least the highest number under the
// parent, so the next allocation reuses no id (MTIX-95.31.27).
func TestImport_ConcurrentMergeImports_SequenceCoversMaxID(t *testing.T) {
	data := makeTestExport(t, newTestStore(t)) // IMP-1, IMP-2
	dst := newTestStore(t)
	for i, err := range runConcurrentMergeImports(t, dst, data, 4) {
		require.NoError(t, err, "import %d", i)
	}
	seq, err := dst.NextSequence(context.Background(), "IMP:")
	require.NoError(t, err)
	assert.Equal(t, 3, seq, "highest imported number is 2, so the next is 3")
}

// TestImport_MergeImport_RaisesCounterBelowImportedIDs verifies a counter
// that starts below the imported node numbers is raised to the highest one,
// so a later create does not collide with an imported id (MTIX-95.31.27).
func TestImport_MergeImport_RaisesCounterBelowImportedIDs(t *testing.T) {
	ctx := context.Background()
	data := makeTestExport(t, newTestStore(t)) // IMP-1, IMP-2
	dst := newTestStore(t)
	_, err := dst.WriteDB().ExecContext(ctx, "INSERT INTO sequences (key, value) VALUES ('IMP:', 1)")
	require.NoError(t, err)
	_, err = dst.Import(ctx, data, sqlite.ImportModeMerge, true)
	require.NoError(t, err)
	// Read the stored counter: NextSequence skips ids already taken, so it
	// would hide a counter that was not raised.
	var value int
	require.NoError(t, dst.ReadDB().QueryRowContext(ctx,
		"SELECT value FROM sequences WHERE key = 'IMP:'").Scan(&value))
	assert.Equal(t, 2, value, "counter raised from 1 to the highest imported number")
}

// TestImport_MergeImport_NeverLowersExistingCounter verifies a counter above
// the highest node number is kept, so ids a deleted task held stay unused.
func TestImport_MergeImport_NeverLowersExistingCounter(t *testing.T) {
	ctx := context.Background()
	data := makeTestExport(t, newTestStore(t)) // IMP-1, IMP-2
	dst := newTestStore(t)
	_, err := dst.WriteDB().ExecContext(ctx, "INSERT INTO sequences (key, value) VALUES ('IMP:', 40)")
	require.NoError(t, err)
	_, err = dst.Import(ctx, data, sqlite.ImportModeMerge, true)
	require.NoError(t, err)
	seq, err := dst.NextSequence(ctx, "IMP:")
	require.NoError(t, err)
	assert.Equal(t, 41, seq)
}

// TestImport_SequenceRebuildFails_RollsBackImport verifies a failed counter
// rebuild fails the whole import and writes nothing, instead of committing
// nodes whose counters are behind them (MTIX-95.31.27).
func TestImport_SequenceRebuildFails_RollsBackImport(t *testing.T) {
	ctx := context.Background()
	data := makeTestExport(t, newTestStore(t))
	dst := newTestStore(t)
	_, err := dst.WriteDB().ExecContext(ctx,
		`CREATE TRIGGER test_fail_sequences BEFORE INSERT ON sequences
		 BEGIN SELECT RAISE(ABORT, 'test: sequence rebuild fails'); END`)
	require.NoError(t, err)
	_, err = dst.Import(ctx, data, sqlite.ImportModeMerge, true)
	require.Error(t, err)
	assert.NotErrorIs(t, err, sqlite.ErrImportIncomplete)
	var n int
	require.NoError(t, dst.ReadDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM nodes").Scan(&n))
	assert.Zero(t, n, "the failed import wrote no node")
}

// TestImport_MergeImport_RaisesNestedAndRootCounters verifies that counters
// for a parent key and the root key, both below the imported numbers, are
// raised to the highest imported child and root number (MTIX-95.31.27).
func TestImport_MergeImport_RaisesNestedAndRootCounters(t *testing.T) {
	ctx := context.Background()
	src := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, n := range []struct {
		id, parent string
		depth, seq int
	}{{"PRE-1", "", 0, 1}, {"PRE-1.1", "PRE-1", 1, 1}, {"PRE-1.3", "PRE-1", 1, 3}} {
		require.NoError(t, src.CreateNode(ctx, &model.Node{
			ID: n.id, ParentID: n.parent, Project: "PRE", Depth: n.depth, Seq: n.seq,
			Title: n.id, Status: model.StatusOpen, Priority: model.PriorityMedium,
			Weight: 1.0, NodeType: model.NodeTypeIssue, ContentHash: "h" + n.id,
			CreatedAt: now, UpdatedAt: now,
		}))
	}
	data, err := src.Export(ctx, "PRE", "0.1.0")
	require.NoError(t, err)

	dst := newTestStore(t)
	_, err = dst.WriteDB().ExecContext(ctx,
		"INSERT INTO sequences (key, value) VALUES ('PRE:', 0), ('PRE:PRE-1', 1)")
	require.NoError(t, err)
	_, err = dst.Import(ctx, data, sqlite.ImportModeMerge, true)
	require.NoError(t, err)

	counter := func(key string) int {
		var v int
		require.NoError(t, dst.ReadDB().QueryRowContext(ctx,
			"SELECT value FROM sequences WHERE key = ?", key).Scan(&v))
		return v
	}
	assert.Equal(t, 3, counter("PRE:PRE-1"), "child counter raised to the highest child number")
	assert.Equal(t, 1, counter("PRE:"), "root counter raised to the highest root number")
}

// TestImportReconcile_Renumber_DoesNotKeepTemporaryNumberInCounter verifies
// that the temporary number a renumbered local task is parked at does not
// stay in the counter. A local child numbered 40 under another parent must
// not push the root counter: after the renumber the next root number is 7
// (the moved task holds REC-6), as before the counters were never lowered
// (MTIX-95.31.27, FR-7.8.x).
func TestImportReconcile_Renumber_DoesNotKeepTemporaryNumberInCounter(t *testing.T) {
	ctx := context.Background()
	local, file, _, _ := sameIDStores(t)
	createSameIDTask(t, local, "REC-1.40", "REC-1", 40, taskUID(t), "High child under another parent")

	_, _, err := local.ImportReconcile(ctx, file(), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)

	var root, other int
	require.NoError(t, local.ReadDB().QueryRowContext(ctx,
		"SELECT value FROM sequences WHERE key = 'REC:'").Scan(&root))
	assert.Equal(t, 6, root, "the root counter is the highest root number, not the parking number")
	require.NoError(t, local.ReadDB().QueryRowContext(ctx,
		"SELECT value FROM sequences WHERE key = 'REC:REC-1'").Scan(&other))
	assert.Equal(t, 40, other, "the child counter keeps the highest child number")
	next, err := local.NextSequence(ctx, "REC:")
	require.NoError(t, err)
	assert.Equal(t, 7, next)
}

// TestImportReconcile_Renumber_KeepsHigherExistingCounter verifies the
// restore after a renumber never lowers a counter that was above the
// highest number under its parent before the import.
func TestImportReconcile_Renumber_KeepsHigherExistingCounter(t *testing.T) {
	ctx := context.Background()
	local, file, _, _ := sameIDStores(t)
	_, err := local.WriteDB().ExecContext(ctx,
		"INSERT INTO sequences (key, value) VALUES ('REC:', 20) ON CONFLICT(key) DO UPDATE SET value = 20")
	require.NoError(t, err)
	_, _, err = local.ImportReconcile(ctx, file(), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	var root int
	require.NoError(t, local.ReadDB().QueryRowContext(ctx,
		"SELECT value FROM sequences WHERE key = 'REC:'").Scan(&root))
	assert.Equal(t, 20, root, "a counter above the highest number is kept")
}

// TestImport_MergeImport_SoftDeletedNodeReservesItsNumber verifies a
// soft-deleted node in the file still sets the counter: the rebuild counts
// deleted rows, so its number is never handed out again (MTIX-95.31.27).
func TestImport_MergeImport_SoftDeletedNodeReservesItsNumber(t *testing.T) {
	ctx := context.Background()
	src := newTestStore(t)
	makeTestExport(t, src) // IMP-1, IMP-2
	require.NoError(t, src.DeleteNode(ctx, "IMP-2", false, "agent-test"))
	data, err := src.Export(ctx, "IMP", "0.1.0")
	require.NoError(t, err)
	dst := newTestStore(t)
	_, err = dst.Import(ctx, data, sqlite.ImportModeMerge, true)
	require.NoError(t, err)
	var value int
	require.NoError(t, dst.ReadDB().QueryRowContext(ctx,
		"SELECT value FROM sequences WHERE key = 'IMP:'").Scan(&value))
	assert.Equal(t, 2, value, "the soft-deleted IMP-2 still reserves number 2")
}
