// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestQueueCap_SetMaxQueueSize_IsWhatTheEmitPathEnforces: the cap the command
// layer stores is the cap a write that journals an event meets, and the
// refusal names the command that raises it (MTIX-112.17.16.16).
func TestQueueCap_SetMaxQueueSize_IsWhatTheEmitPathEnforces(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.SetMaxQueueSize(context.Background(), 1))
	createPlainTask(t, s, "REC-1", 1, "", sameIDTime)

	err := s.CreateNode(context.Background(), &model.Node{
		ID: "REC-2", Project: "REC", Seq: 2, Title: "Second", Status: model.StatusOpen, Priority: model.PriorityMedium,
		Weight: 1, NodeType: model.NodeTypeForDepth(0), CreatedAt: sameIDTime, UpdatedAt: sameIDTime,
	})

	require.True(t, errors.Is(err, model.ErrSyncQueueFull), "%v", err)
	require.Contains(t, err.Error(), "mtix config set sync.max_queue_size")
	require.NotContains(t, err.Error(), "push --force", "push --force only bypasses the push lock, it does not drain the queue")
}

// TestQueueCap_ImportAloneOverTheCap_NamesTheCapRaiseThatClearsIt: when the
// import alone needs more events than the cap, the refusal names the value to
// set, and setting it lets the same import run (principle C).
func TestQueueCap_ImportAloneOverTheCap_NamesTheCapRaiseThatClearsIt(t *testing.T) {
	s := syncedStoreWithTask(t)
	require.NoError(t, s.SetMaxQueueSize(context.Background(), 1))
	data := retitled(t, s, "REC-1", "Imported title")
	data.Nodes[0].Description = "Imported description"
	data.Nodes[0].ContentHash = model.ComputeContentHash("Imported title", "Imported description", "", "", nil)
	require.NoError(t, recomputeChecksum(data))

	err := mergeErr(s, data)

	require.True(t, errors.Is(err, model.ErrSyncQueueFull), "%v", err)
	require.Contains(t, err.Error(), "mtix config set sync.max_queue_size 2",
		"the import needs two events on an empty queue, and the fix names that value")
	require.NoError(t, s.SetMaxQueueSize(context.Background(), 2), "the printed fix")
	require.NoError(t, mergeErr(s, data), "the same import runs once the cap is raised")
}
