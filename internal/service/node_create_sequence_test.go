// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// TestCreateNode_SkippedNumberAlsoTaken_SkipsOnlyOnce: the skip checks
// the number it hands out once and does not loop. A test trigger stands in
// for a pulled PROJ-1.3 that lands between the skip and the insert (the
// allocate/insert race, MTIX-107.57): the create that skipped from 1 to 3
// fails with ErrAlreadyExists, the counter stays at 3, and the next create
// succeeds with PROJ-1.4.
func TestCreateNode_SkippedNumberAlsoTaken_SkipsOnlyOnce(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	createPROJs(t, svc, "", 1)
	createPROJs(t, svc, "PROJ-1", 2)
	setSequenceCounter(t, st, "PROJ:PROJ-1", 0)
	_, err := st.WriteDB().ExecContext(context.Background(), `CREATE TRIGGER test_pulled_after_skip
		AFTER UPDATE OF value ON sequences WHEN NEW.key = 'PROJ:PROJ-1' AND OLD.value = 1 AND NEW.value = 3
		BEGIN
		  INSERT INTO nodes (id, parent_id, depth, seq, project, title, status, created_at, updated_at)
		  VALUES ('PROJ-1.3', 'PROJ-1', 1, 3, 'PROJ', 'pulled', 'open', '2026-03-10T12:00:00Z', '2026-03-10T12:00:00Z');
		END`)
	require.NoError(t, err)

	_, err = createPROJ(svc, "PROJ-1", "new")

	require.ErrorIs(t, err, model.ErrAlreadyExists)
	require.Equal(t, 3, sequenceCounterValue(t, st, "PROJ:PROJ-1"), "one skip, no loop")
	node, err := createPROJ(svc, "PROJ-1", "retry")
	require.NoError(t, err)
	require.Equal(t, "PROJ-1.4", node.ID)
}

// TestCreateNode_CounterBehindConcurrentCreates_DistinctContiguousNumbers:
// concurrent creates against a counter behind five tasks all succeed, with
// distinct numbers that continue after the highest with no gap, under a
// root key and a child key.
func TestCreateNode_CounterBehindConcurrentCreates_DistinctContiguousNumbers(t *testing.T) {
	tests := []struct {
		name, parent, key string
		prefix            string
	}{
		{"root key", "", "PROJ:", "PROJ-"},
		{"child key", "PROJ-1", "PROJ:PROJ-1", "PROJ-1."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, st, _ := newTestNodeService(t)
			if tt.parent != "" {
				createPROJs(t, svc, "", 1)
			}
			createPROJs(t, svc, tt.parent, 5)
			setSequenceCounter(t, st, tt.key, 0)
			const creates = 8

			var wg sync.WaitGroup
			ids := make([]string, creates)
			errs := make([]error, creates)
			for i := 0; i < creates; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					node, err := createPROJ(svc, tt.parent, fmt.Sprintf("concurrent %d", i))
					errs[i] = err
					if err == nil {
						ids[i] = node.ID
					}
				}(i)
			}
			wg.Wait()

			for i, err := range errs {
				require.NoError(t, err, "create %d", i)
			}
			want := make([]string, creates)
			for i := range want {
				want[i] = fmt.Sprintf("%s%d", tt.prefix, 6+i)
			}
			require.ElementsMatch(t, want, ids)
			require.Equal(t, 5+creates, sequenceCounterValue(t, st, tt.key))
		})
	}
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
