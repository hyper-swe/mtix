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

// newUnclaimToolFixture registers the workflow tools as author with TEST-1
// claimed by agent-a (MTIX-169).
func newUnclaimToolFixture(t *testing.T, author string) (*ToolRegistry, *service.NodeService, string) {
	t.Helper()
	st := newInboxTestStore(t)
	svc := service.NewNodeService(st, nil, nil, nil, fixedClock)
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, svc, service.NewBackgroundService(st, nil, nil, fixedClock), WithAuthor(author))
	n, err := svc.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "TEST", Title: "claimed"})
	require.NoError(t, err)
	require.NoError(t, svc.ClaimNode(context.Background(), n.ID, "agent-a"))
	return reg, svc, n.ID
}

// TestUnclaimTool_Cases_EnforceAssignee verifies mtix_unclaim resolves the
// actor and refuses a caller that does not hold the claim (MTIX-169).
func TestUnclaimTool_Cases_EnforceAssignee(t *testing.T) {
	tests := []struct {
		name     string
		author   string
		args     map[string]any
		want     error
		assignee string
	}{
		{"server_identity_non_assignee", "agent-b", map[string]any{}, model.ErrAlreadyClaimed, "agent-a"},
		{"no_identity", "", map[string]any{}, model.ErrInvalidInput, "agent-a"},
		{"agent_id_assignee", "agent-b", map[string]any{"agent_id": "agent-a"}, nil, ""},
		{"force_non_assignee", "agent-b", map[string]any{"force": true}, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, svc, id := newUnclaimToolFixture(t, tc.author)
			tc.args["id"], tc.args["reason"] = id, "taking over"
			raw, err := json.Marshal(tc.args)
			require.NoError(t, err)

			_, err = reg.Call(context.Background(), "mtix_unclaim", raw)

			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			} else {
				require.NoError(t, err)
			}
			got, getErr := svc.GetNode(context.Background(), id)
			require.NoError(t, getErr)
			require.Equal(t, tc.assignee, got.Assignee)
		})
	}
}
