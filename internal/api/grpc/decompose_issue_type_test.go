// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestHandleDecompose_IssueType_PersistedAndValidated(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()
	parent := createTestNode(t, s, "Parent", "DECOMP")

	_, err := s.HandleDecompose(ctx, parent.ID, "a", []DecomposeChildReq{{Title: "x", IssueType: "epic"}})
	require.Error(t, err)
	kids, err := s.nodeSvc.GetDirectChildren(ctx, parent.ID)
	require.NoError(t, err)
	assert.Empty(t, kids)

	ids, err := s.HandleDecompose(ctx, parent.ID, "a", []DecomposeChildReq{{Title: "x", IssueType: model.IssueTypeBug}, {Title: "y"}})
	require.NoError(t, err)
	n, err := s.nodeSvc.GetNode(ctx, ids[0])
	require.NoError(t, err)
	assert.Equal(t, model.IssueTypeBug, n.IssueType)
	n, err = s.nodeSvc.GetNode(ctx, ids[1])
	require.NoError(t, err)
	assert.Equal(t, model.IssueType(""), n.IssueType)
}
