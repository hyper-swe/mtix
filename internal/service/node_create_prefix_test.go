// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Local create boundaries share FR-2.1a prefix validation (MTIX-107.3).
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestCreateNode_InvalidPrefix_ReturnsSharedErrorWithoutWrites(t *testing.T) {
	for _, prefix := range []string{"TEST_BAD", "test", "", "1TEST", "ABCDEFGHIJKLMNOPQRSTU", "TEST%", "TEST.X", "TÉST"} {
		t.Run(prefix, func(t *testing.T) {
			svc, st, bc := newTestNodeService(t)
			node, err := svc.CreateNode(context.Background(), &service.CreateNodeRequest{Title: "Invalid prefix", Project: prefix})
			require.ErrorIs(t, err, model.ErrInvalidInput)
			assert.EqualError(t, err, model.ValidatePrefix(prefix).Error())
			assert.Nil(t, node)
			assert.Empty(t, bc.Events())
			for _, query := range []string{"SELECT COUNT(*) FROM nodes", "SELECT COUNT(*) FROM sequences", "SELECT COUNT(*) FROM sync_events"} {
				var count int
				require.NoError(t, st.WriteDB().QueryRowContext(context.Background(), query).Scan(&count))
				assert.Zero(t, count, query)
			}
		})
	}
}

func TestCreateNode_ValidPrefixBoundary_CreatesNode(t *testing.T) {
	for _, prefix := range []string{"A", "TEST-DEV-OPS", "ABCDEFGHIJKLMNOPQRST"} {
		t.Run(prefix, func(t *testing.T) {
			svc, st, _ := newTestNodeService(t)
			node, err := svc.CreateNode(context.Background(), &service.CreateNodeRequest{Title: "Valid prefix", Project: prefix})
			require.NoError(t, err)
			assert.Equal(t, prefix+"-1", node.ID)
			stored, err := st.GetNode(context.Background(), node.ID)
			require.NoError(t, err)
			assert.Equal(t, prefix, stored.Project)
		})
	}
}

func TestCreateNode_InvalidInheritedPrefix_RejectsValidSuppliedProject(t *testing.T) {
	for _, parentProject := range []string{"TEST_BAD", "TEST"} {
		t.Run(parentProject, func(t *testing.T) {
			svc, st, bc := newTestNodeService(t)
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			require.NoError(t, st.CreateNode(t.Context(), &model.Node{
				ID: "TEST_BAD-1", Project: parentProject, Seq: 1, Title: "Legacy parent",
				Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1,
				NodeType: model.NodeTypeEpic, CreatedAt: now, UpdatedAt: now,
			}))
			n, err := svc.CreateNode(t.Context(), &service.CreateNodeRequest{Title: "Local child", ParentID: "TEST_BAD-1", Project: "TEST"})
			require.ErrorIs(t, err, model.ErrInvalidInput)
			assert.Contains(t, err.Error(), "invalid inherited project prefix")
			assert.Contains(t, err.Error(), "TEST_BAD")
			assert.Nil(t, n)
			_, err = st.GetNode(t.Context(), "TEST_BAD-1.1")
			assert.ErrorIs(t, err, model.ErrNotFound)
			var count int
			require.NoError(t, st.WriteDB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sequences").Scan(&count))
			assert.Zero(t, count)
			assert.Empty(t, bc.Events())
		})
	}
}
