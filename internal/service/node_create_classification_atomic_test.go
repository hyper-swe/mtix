// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Classified creates preserve transactional allocation and initial claims (FR-2.7/FR-11.2a).
package service_test

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestCreateNode_ClassifiedRefusal_PreservesAtomicAllocationAndClaim(t *testing.T) {
	for _, failure := range []string{"invalid priority", "parent claim event"} {
		t.Run(failure, func(t *testing.T) { checkClassifiedCreateRollback(t, failure) })
	}
}

func checkClassifiedCreateRollback(t *testing.T, failure string) {
	t.Helper()
	st, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	bc := newRecordingBroadcaster()
	svc := service.NewNodeService(st, bc, &service.StaticConfig{AutoClaimEnabled: true}, nil,
		fixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
	ctx := context.Background()
	parent, err := svc.CreateNode(ctx, createClassifiedRequest(t, "feature", "parent-worker"))
	require.NoError(t, err)
	parent, err = st.GetNode(ctx, parent.ID)
	require.NoError(t, err)
	req := createClassifiedRequest(t, "bug", "")
	req.ParentID = parent.ID
	before := classifiedCreateCounts(t, st)
	bc.Reset()
	if failure == "invalid priority" {
		req.Priority = 9
	} else {
		_, err = st.WriteDB().ExecContext(ctx, `CREATE TRIGGER reject_classified_claim BEFORE INSERT ON sync_events WHEN NEW.op_type = 'claim' BEGIN SELECT RAISE(ABORT, 'claim refused'); END`)
		require.NoError(t, err)
	}
	_, err = svc.CreateNode(ctx, req)
	require.Error(t, err)
	assert.Equal(t, before, classifiedCreateCounts(t, st))
	assert.Zero(t, sequenceCounterValue(t, st, "TEST:"+parent.ID))
	assert.Empty(t, bc.Events())
	unchanged, err := st.GetNode(ctx, parent.ID)
	require.NoError(t, err)
	assert.Equal(t, parent, unchanged)
	var current string
	require.NoError(t, st.QueryRow(ctx, `SELECT current_node_id FROM agents WHERE agent_id = ?`, "parent-worker").Scan(&current))
	assert.Equal(t, parent.ID, current)
	if failure == "parent claim event" {
		_, err = st.WriteDB().ExecContext(ctx, `DROP TRIGGER reject_classified_claim`)
		require.NoError(t, err)
	}
	req.Priority = 2
	child, err := svc.CreateNode(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, parent.ID+".1", child.ID)
	assert.Equal(t, model.IssueTypeBug, child.IssueType)
	assert.Equal(t, "parent-worker", child.Assignee)
	assert.Equal(t, model.StatusInProgress, child.Status)
	events := bc.Events()
	require.Len(t, events, 2)
	assert.Equal(t, service.EventNodeCreated, events[0].Type)
	assert.Equal(t, service.EventNodeClaimed, events[1].Type)
	checkClassifiedChildRoundTrip(t, svc, st, child)
}

func classifiedCreateCounts(t *testing.T, st *sqlite.Store) []int {
	t.Helper()
	counts := make([]int, 4)
	for i, query := range []string{`SELECT COUNT(*) FROM nodes`, `SELECT COUNT(*) FROM sync_events`, `SELECT COUNT(*) FROM agents`, `SELECT COUNT(*) FROM sequences`} {
		require.NoError(t, st.QueryRow(context.Background(), query).Scan(&counts[i]))
	}
	return counts
}

func checkClassifiedChildRoundTrip(t *testing.T, svc *service.NodeService, st *sqlite.Store, child *model.Node) {
	t.Helper()
	ctx := context.Background()
	value := model.IssueTypeRefactor
	require.NoError(t, svc.ApplyUpdate(ctx, child.ID, &service.NodeUpdate{IssueType: &value}))
	got, err := st.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, value, got.IssueType)
	assert.Equal(t, child.ContentHash, got.ContentHash)
	exported, err := st.Export(ctx, "TEST", "test")
	require.NoError(t, err)
	imported, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, imported.Close()) })
	_, err = imported.Import(ctx, exported, sqlite.ImportModeReplace, true)
	require.NoError(t, err)
	restored, err := imported.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, value, restored.IssueType)
	assert.Equal(t, child.Assignee, restored.Assignee)
	target, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, target.Close()) })
	events, err := st.ReadPendingEvents(ctx, 100)
	require.NoError(t, err)
	for _, event := range events {
		require.NoError(t, target.WithTx(ctx, func(tx *sql.Tx) error { return sqlite.IdempotentApply(ctx, tx, event) }))
	}
	replayed, err := target.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, value, replayed.IssueType)
	assert.Equal(t, child.Assignee, replayed.Assignee)
}
