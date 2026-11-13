// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHoldsNodes_OnlySoftDeletedTask_ReportsTrue pins the parity with the old
// row count (MTIX-112.17.16.16): a store holding only a soft-deleted task is
// not empty, so a replace import into it is still guarded.
func TestHoldsNodes_OnlySoftDeletedTask_ReportsTrue(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	holds, err := s.HoldsNodes(ctx)
	require.NoError(t, err)
	assert.False(t, holds, "a fresh store is empty")

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Gone", now)))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin-user"))

	holds, err = s.HoldsNodes(ctx)
	require.NoError(t, err)
	assert.True(t, holds, "a soft-deleted row still counts as a held task")
}
