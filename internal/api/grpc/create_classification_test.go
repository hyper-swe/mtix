// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// gRPC DTO creation preserves optional issue classification and explicit assignee.
package grpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestCreateRPC_ClassificationAndAssignment(t *testing.T) {
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", "", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			s := testGRPCServer(t)
			raw, err := json.Marshal(map[string]string{"title": "Classified work", "project": "TEST", "creator": "author", "issue_type": kind, "assignee": "worker"})
			require.NoError(t, err)
			var req CreateNodeReq
			require.NoError(t, json.Unmarshal(raw, &req))
			n, err := s.HandleCreateNode(context.Background(), &req)
			if kind == "invalid" {
				require.Equal(t, codes.InvalidArgument, status.Code(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, model.IssueType(kind), n.IssueType)
			assert.Equal(t, model.NodeTypeEpic, n.NodeType)
			assert.Equal(t, "worker", n.Assignee)
			assert.Equal(t, "author", n.Creator)
			assert.Equal(t, model.StatusInProgress, n.Status)
			got, err := s.store.GetNode(context.Background(), n.ID)
			require.NoError(t, err)
			assert.Equal(t, n.IssueType, got.IssueType)
			assert.Equal(t, n.Assignee, got.Assignee)
		})
	}
}
