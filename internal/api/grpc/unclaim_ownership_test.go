// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestHandleUnclaim_NonAssignee_ReturnsFailedPrecondition verifies the gRPC
// unclaim refuses an agent that does not hold the claim (MTIX-169).
func TestHandleUnclaim_NonAssignee_ReturnsFailedPrecondition(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()
	n, err := s.HandleCreateNode(ctx, &CreateNodeReq{Project: "TEST", Title: "claimed"})
	require.NoError(t, err)
	_, err = s.HandleClaim(ctx, n.ID, "agent-a", false)
	require.NoError(t, err)

	_, err = s.HandleUnclaim(ctx, n.ID, "taking over", "agent-b")

	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, err.Error(), "agent-a")
	got, getErr := s.store.GetNode(ctx, n.ID)
	require.NoError(t, getErr)
	require.Equal(t, "agent-a", got.Assignee)
}
