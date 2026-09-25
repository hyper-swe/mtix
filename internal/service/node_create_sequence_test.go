// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// A local create never fails with "already exists" because the sequence
// counter of its parent fell behind the nodes (MTIX-95.38): an import
// interrupted before it rebuilt the counters, a pull or clone before
// MTIX-95.38, or any other cause. When the number the counter reaches is
// taken, the create moves the counter past the highest number under that
// parent, once, and uses that number, so no number is skipped on success.
// The tests put a counter behind by writing it directly.

// setSequenceCounter sets the counter key on st to value, or removes it
// when value is negative.
func setSequenceCounter(t *testing.T, st *sqlite.Store, key string, value int) {
	t.Helper()
	ctx := context.Background()
	var err error
	if value < 0 {
		_, err = st.WriteDB().ExecContext(ctx, `DELETE FROM sequences WHERE key = ?`, key)
	} else {
		_, err = st.WriteDB().ExecContext(ctx,
			`INSERT INTO sequences (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	}
	require.NoError(t, err)
}

// sequenceCounterValue returns the counter key on st, 0 when it has no row.
func sequenceCounterValue(t *testing.T, st *sqlite.Store, key string) int {
	t.Helper()
	var v int
	err := st.ReadDB().QueryRowContext(context.Background(),
		`SELECT value FROM sequences WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	require.NoError(t, err)
	return v
}

// createPROJ creates a PROJ task titled title under parent through svc.
func createPROJ(svc *service.NodeService, parent, title string) (*model.Node, error) {
	return svc.CreateNode(context.Background(), &service.CreateNodeRequest{
		ParentID: parent, Project: "PROJ", Title: title, Creator: "agent",
	})
}

// createPROJs creates count PROJ tasks under parent through svc.
func createPROJs(t *testing.T, svc *service.NodeService, parent string, count int) {
	t.Helper()
	for i := 1; i <= count; i++ {
		_, err := createPROJ(svc, parent, fmt.Sprintf("existing %d", i))
		require.NoError(t, err)
	}
}

// TestCreateNode_CounterBehindTakenNumber_UsesNextFreeNumber: the number
// the counter reaches is taken, so the create uses the number after the
// highest under the parent, counting soft-deleted tasks, at the first
// attempt; the counter ends at that number, so the next create takes the
// one after it.
func TestCreateNode_CounterBehindTakenNumber_UsesNextFreeNumber(t *testing.T) {
	tests := []struct {
		name   string
		parent string
		setup  func(t *testing.T, svc *service.NodeService, st *sqlite.Store)
		key    string
		want   []string
	}{
		{"root counter lowered", "", func(t *testing.T, svc *service.NodeService, st *sqlite.Store) {
			createPROJs(t, svc, "", 3)
			setSequenceCounter(t, st, "PROJ:", 1)
		}, "PROJ:", []string{"PROJ-4", "PROJ-5"}},
		{"child counter lost", "PROJ-1", func(t *testing.T, svc *service.NodeService, st *sqlite.Store) {
			createPROJs(t, svc, "", 1)
			createPROJs(t, svc, "PROJ-1", 2)
			setSequenceCounter(t, st, "PROJ:PROJ-1", -1)
		}, "PROJ:PROJ-1", []string{"PROJ-1.3", "PROJ-1.4"}},
		{"taken and higher numbers held by soft-deleted tasks", "",
			func(t *testing.T, svc *service.NodeService, st *sqlite.Store) {
				createPROJs(t, svc, "", 5)
				for _, id := range []string{"PROJ-4", "PROJ-5"} {
					require.NoError(t, svc.DeleteNode(context.Background(), id, false, "agent"))
				}
				setSequenceCounter(t, st, "PROJ:", 3)
			}, "PROJ:", []string{"PROJ-6", "PROJ-7"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, st, _ := newTestNodeService(t)
			tt.setup(t, svc, st)

			for i, want := range tt.want {
				node, err := createPROJ(svc, tt.parent, fmt.Sprintf("new %d", i+1))
				require.NoError(t, err, "create %d succeeds at the first attempt", i+1)
				require.Equal(t, want, node.ID)
				require.Equal(t, node.Seq, sequenceCounterValue(t, st, tt.key), "no number is skipped")
			}
		})
	}
}

// TestCreateNode_CounterBehindFreeNumber_UsesAllocatedNumber: the number
// the counter reaches is free, so the create uses it even though a higher
// number is taken; the skip happens only when the number is taken.
func TestCreateNode_CounterBehindFreeNumber_UsesAllocatedNumber(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	createPROJs(t, svc, "", 2)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	held := &model.Node{
		ID: "PROJ-5", Project: "PROJ", Seq: 5, Title: "imported", NodeType: model.NodeTypeForDepth(0),
		Priority: model.PriorityMedium, Status: model.StatusOpen, Weight: 1.0, CreatedAt: now, UpdatedAt: now,
	}
	held.ContentHash = held.ComputeHash()
	require.NoError(t, st.CreateNode(context.Background(), held))

	node, err := createPROJ(svc, "", "new")

	require.NoError(t, err)
	require.Equal(t, "PROJ-3", node.ID)
}

// TestCreateNode_SkippedNumberAlsoTaken_SkipsOnlyOnce: in a store whose
// PROJ-2 records the number 0, the skip past the highest recorded number
// (1) lands on the taken PROJ-2. The create skips once, not in a loop, and
// fails with ErrAlreadyExists; the counter has moved on, so the next create
// succeeds.
func TestCreateNode_SkippedNumberAlsoTaken_SkipsOnlyOnce(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	createPROJs(t, svc, "", 1)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	odd := &model.Node{
		ID: "PROJ-2", Project: "PROJ", Seq: 0, Title: "number not recorded", NodeType: model.NodeTypeForDepth(0),
		Priority: model.PriorityMedium, Status: model.StatusOpen, Weight: 1.0, CreatedAt: now, UpdatedAt: now,
	}
	odd.ContentHash = odd.ComputeHash()
	require.NoError(t, st.CreateNode(context.Background(), odd))
	setSequenceCounter(t, st, "PROJ:", 0)

	_, err := createPROJ(svc, "", "new")

	require.ErrorIs(t, err, model.ErrAlreadyExists)
	require.Equal(t, 2, sequenceCounterValue(t, st, "PROJ:"), "one skip, to the number after the highest recorded")
	node, err := createPROJ(svc, "", "retry")
	require.NoError(t, err)
	require.Equal(t, "PROJ-3", node.ID)
}

// TestCreateNode_CounterBehindConcurrentCreates_DistinctContiguousNumbers:
// concurrent creates against a counter behind five tasks all succeed, with
// distinct numbers that continue after the highest with no gap.
func TestCreateNode_CounterBehindConcurrentCreates_DistinctContiguousNumbers(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	createPROJs(t, svc, "", 5)
	setSequenceCounter(t, st, "PROJ:", 0)
	const creates = 8

	var wg sync.WaitGroup
	seqs := make([]int, creates)
	errs := make([]error, creates)
	for i := 0; i < creates; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			node, err := createPROJ(svc, "", fmt.Sprintf("concurrent %d", i))
			errs[i] = err
			if err == nil {
				seqs[i] = node.Seq
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "create %d", i)
	}
	sort.Ints(seqs)
	want := make([]int, creates)
	for i := range want {
		want[i] = 6 + i
	}
	require.Equal(t, want, seqs)
	require.Equal(t, 5+creates, sequenceCounterValue(t, st, "PROJ:"))
}

// TestDecompose_CounterBehind_ChildrenContinueAfterHighest: decompose
// allocates each child's number the same way, so it succeeds under a
// parent whose counter was lost.
func TestDecompose_CounterBehind_ChildrenContinueAfterHighest(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	createPROJs(t, svc, "", 1)
	createPROJs(t, svc, "PROJ-1", 2)
	setSequenceCounter(t, st, "PROJ:PROJ-1", -1)

	ids, err := svc.Decompose(context.Background(), "PROJ-1",
		[]service.DecomposeInput{{Title: "first"}, {Title: "second"}}, "agent")

	require.NoError(t, err)
	require.Equal(t, []string{"PROJ-1.3", "PROJ-1.4"}, ids)
}
