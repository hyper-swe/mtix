// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package relaygate_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

var errKilled = errors.New("process killed (-9 simulation)")

// killStore simulates kill -9 after the publisher appended and before its
// cursor advance (FR-21 §6.2).
type killStore struct{ tick.StackStore }

func (k *killStore) AdvanceRelayPushCursor(context.Context, int64, uint64) error {
	panic(errKilled)
}

// survive runs one pass of a process that may be killed: a kill is a panic
// carrying errKilled, after which nothing else of that process runs.
func survive(t *testing.T, stack *tick.Stack) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			if err, ok := r.(error); ok && errors.Is(err, errKilled) {
				return
			}
			panic(r)
		}
	}()
	_, _ = stack.Pass(t.Context()) // the killed process reports nothing
}

// killedPublishKeeping publishes peer i's pending events with the publisher
// dying before the cursor advance, then keeps keep(newBytes) bytes of what it
// appended: the file a kill -9 leaves behind (newBytes means nothing lost).
func killedPublishKeeping(t *testing.T, g gateRelay, i int, keep func(newBytes int64) int64) {
	t.Helper()
	files := g.segments(t, i)
	var before, beforeNo int64
	if len(files) > 0 {
		before, beforeNo = files[len(files)-1].Size, int64(files[len(files)-1].No)
	}
	cfg := g.peers[i]
	cfg.Store = &killStore{StackStore: cfg.Store}
	stack, err := tick.Open(cfg)
	require.NoError(t, err)
	survive(t, stack)
	files = g.segments(t, i)
	last := files[len(files)-1]
	if int64(last.No) != beforeNo {
		before = 0 // the kill came while a new segment was being created
	}
	if last.Size > before {
		require.NoError(t, os.Truncate(last.Path, before+keep(last.Size-before)))
	}
}

// eventCounts returns the journal rows and distinct event ids on peer i.
func eventCounts(t *testing.T, g gateRelay, i int) (rows, distinct int) {
	t.Helper()
	require.NoError(t, g.sql(i).ReadDB().QueryRowContext(t.Context(),
		`SELECT COUNT(*), COUNT(DISTINCT event_id) FROM sync_events`).Scan(&rows, &distinct))
	return rows, distinct
}

func createCrashNode(t *testing.T, st *sqlite.Store, seq int) {
	t.Helper()
	id := fmt.Sprintf("TEST-%d", seq)
	node := testutil.MakeNode(t, testutil.WithTitle(id))
	node.ID, node.Seq, node.Creator, node.NodeType = id, seq, "planner", model.NodeTypeStory
	node.ContentHash = model.ComputeContentHash(id, "", "", "", nil)
	require.NoError(t, st.CreateNode(t.Context(), node))
}

func crashSegments(cfg tick.StackConfig) string {
	return filepath.Join(cfg.RelayDir, tick.PeersDirName, cfg.PeerID, tick.SegmentsDirName)
}

// newRelayGateStore is a store whose machine identity is seeded, so event
// emission has no host network dependency.
func newRelayGateStore(t *testing.T, id string, now func() time.Time) *sqlite.Store {
	t.Helper()
	st := testutil.NewTestStore(t).(*sqlite.Store)
	st.SetClock(now)
	result, err := st.WriteDB().ExecContext(t.Context(), `UPDATE meta SET value = ? WHERE key = ?`,
		id, "meta.sync.machine_hash")
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	return st
}
