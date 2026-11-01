// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// newSyncToolWithHostileEvents returns a registry whose sync workflow tool
// runs on the real SyncService over a real store that holds events with
// hostile ids, both queued and held in quarantine.
func newSyncToolWithHostileEvents(t *testing.T) (*mcp.ToolRegistry, *sqlite.Store) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := sqlite.New(t.TempDir(), logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	for i, id := range hostileEventIDs {
		_, err := st.WriteDB().ExecContext(context.Background(), `INSERT INTO sync_events
			(event_id, project_prefix, node_id, op_type, payload, wall_clock_ts, lamport_clock, vector_clock,
			 author_id, author_machine_hash, sync_status, created_at)
			VALUES (?, 'REC', ?, 'create_node', '{}', 1, ?, '{}', 'agent-a', 'm', 'pending', '2026-01-01T00:00:00Z')`,
			id, "REC-"+strconv.Itoa(i+1), i+1)
		require.NoError(t, err)
	}
	svc := service.NewSyncService(st, logger, time.Now)
	reg := mcp.NewToolRegistry()
	mcp.RegisterSyncWorkflowTool(reg, svc, t.TempDir())
	return reg, st
}

// TestSyncWorkflowTool_RealStoreWithHostileEventIDs_ResultIsPrintableASCII
// drives the real service and store: the tool reports counts only, so a
// stored hostile event id never reaches the agent.
func TestSyncWorkflowTool_RealStoreWithHostileEventIDs_ResultIsPrintableASCII(t *testing.T) {
	reg, _ := newSyncToolWithHostileEvents(t)
	res, err := reg.Call(context.Background(), "mtix_sync_workflow", nil)
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content[0].Text)
	text := res.Content[0].Text
	assert.True(t, isPrintableASCII(strings.ReplaceAll(text, "\n", " ")), "%q", text)
	assert.NotContains(t, text, "INJECTED")
	assert.Contains(t, text, "local_events: 5")
	for _, id := range hostileEventIDs {
		assert.NotContains(t, text, id)
	}
}

// TestSyncWorkflowTool_RealStoreFailure_ResultIsFixedCategory closes the
// store under a store holding hostile ids and checks the real failure text.
func TestSyncWorkflowTool_RealStoreFailure_ResultIsFixedCategory(t *testing.T) {
	reg, st := newSyncToolWithHostileEvents(t)
	require.NoError(t, st.Close())
	res, err := reg.Call(context.Background(), "mtix_sync_workflow", nil)
	require.NoError(t, err)
	require.True(t, res.IsError)
	text := res.Content[0].Text
	assert.True(t, isPrintableASCII(text), "%q", text)
	for _, id := range hostileEventIDs {
		assert.NotContains(t, text, id)
	}
}
