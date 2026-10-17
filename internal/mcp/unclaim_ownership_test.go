// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestUnclaimTool_NonAssignee_ReturnsErrAlreadyClaimed verifies mtix_unclaim
// refuses a caller that does not hold the claim (MTIX-169).
func TestUnclaimTool_NonAssignee_ReturnsErrAlreadyClaimed(t *testing.T) {
	st := newInboxTestStore(t)
	svc := service.NewNodeService(st, nil, nil, nil, fixedClock)
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, svc, service.NewBackgroundService(st, nil, nil, fixedClock), WithAuthor("agent-b"))
	ctx := context.Background()
	n, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "claimed"})
	require.NoError(t, err)
	require.NoError(t, svc.ClaimNode(ctx, n.ID, "agent-a"))

	raw, err := json.Marshal(map[string]string{"id": n.ID, "reason": "taking over"})
	require.NoError(t, err)
	_, err = reg.Call(ctx, "mtix_unclaim", raw)

	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	got, getErr := st.GetNode(ctx, n.ID)
	require.NoError(t, getErr)
	require.Equal(t, "agent-a", got.Assignee)
}
