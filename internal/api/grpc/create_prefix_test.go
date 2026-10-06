// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Local create boundaries share FR-2.1a prefix validation (MTIX-107.3).
package grpc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestCreateRPC_PrefixGrammar_MapsSharedError(t *testing.T) {
	for _, prefix := range []string{"TEST_BAD", "test", "", "1TEST", "ABCDEFGHIJKLMNOPQRSTU", "TEST%", "A", "TEST-DEV-OPS", "ABCDEFGHIJKLMNOPQRST"} {
		t.Run(prefix, func(t *testing.T) {
			s := testGRPCServer(t)
			n, err := s.HandleCreateNode(t.Context(), &CreateNodeReq{Title: "Prefix boundary", Project: prefix})
			if expected := model.ValidatePrefix(prefix); expected != nil {
				require.Equal(t, codes.InvalidArgument, status.Code(err))
				assert.Equal(t, expected.Error(), status.Convert(err).Message())
				assert.Nil(t, n)
				projects, err := s.store.DistinctProjects(t.Context())
				require.NoError(t, err)
				assert.Empty(t, projects)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, prefix, n.Project)
			assert.Equal(t, prefix+"-1", n.ID)
		})
	}
}

func TestCreateRPC_InvalidInheritedPrefix_RejectsValidSuppliedProject(t *testing.T) {
	s := testGRPCServer(t)
	st := s.store
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, st.CreateNode(t.Context(), &model.Node{
		ID: "TEST_BAD-1", Project: "TEST_BAD", Seq: 1, Title: "Legacy parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1,
		NodeType: model.NodeTypeEpic, CreatedAt: now, UpdatedAt: now,
	}))
	n, err := s.HandleCreateNode(t.Context(), &CreateNodeReq{Title: "Local child", ParentID: "TEST_BAD-1", Project: "TEST"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, status.Convert(err).Message(), "invalid inherited project prefix")
	assert.Contains(t, status.Convert(err).Message(), "TEST_BAD")
	assert.Nil(t, n)
	_, err = st.GetNode(t.Context(), "TEST_BAD-1.1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}
