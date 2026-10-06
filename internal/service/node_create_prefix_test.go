// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Local create boundaries share FR-2.1a prefix validation (MTIX-107.3).
package service_test

import (
	"context"
	"testing"

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
