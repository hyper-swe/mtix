// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func progressGRPCChild(t *testing.T, s *testServer, parent, title string) string {
	t.Helper()
	n, err := s.HandleCreateNode(context.Background(), &CreateNodeReq{
		Title: title, ParentID: parent, Project: "TEST", Creator: "tester"})
	require.NoError(t, err)
	return n.ID
}

// TestHandleProgress_ExclusionMetadata_Present covers FR-5.6a and FR-5.6b on gRPC.
func TestHandleProgress_ExclusionMetadata_Present(t *testing.T) {
	s := testGRPCServer(t)
	root := progressGRPCChild(t, s, "", "root")
	a := progressGRPCChild(t, s, root, "a")
	grand := progressGRPCChild(t, s, a, "grand")
	require.NoError(t, s.store.TransitionStatus(t.Context(), grand, model.StatusInvalidated, "r", "t"))

	rep, err := s.HandleProgress(t.Context(), root)
	require.NoError(t, err)
	assert.True(t, rep.HasInvalidatedDescendants)
	assert.Equal(t, 1, rep.InvalidatedCount)
	assert.False(t, rep.AllChildrenExcluded)

	require.NoError(t, s.store.TransitionStatus(t.Context(), a, model.StatusInvalidated, "r", "t"))
	rep, err = s.HandleProgress(t.Context(), root)
	require.NoError(t, err)
	assert.True(t, rep.AllChildrenExcluded)
}

// TestHandleProgress_MissingNode_IsError: a read failure is not a zero.
func TestHandleProgress_MissingNode_IsError(t *testing.T) {
	s := testGRPCServer(t)
	rep, err := s.HandleProgress(t.Context(), "TEST-404")
	require.Error(t, err)
	assert.Nil(t, rep)
}
