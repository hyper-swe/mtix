// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// AssertNodeStatus verifies that a node has the expected status.
// Uses require for preconditions (node must exist) and assert for the status check.
func AssertNodeStatus(t *testing.T, s store.Store, id string, expected model.Status) {
	t.Helper()

	node, err := s.GetNode(context.Background(), id)
	require.NoError(t, err, "AssertNodeStatus: failed to get node %s", id)
	assert.Equal(t, expected, node.Status,
		"AssertNodeStatus: node %s expected status %s, got %s", id, expected, node.Status)
}

// AssertProgress verifies that a node has the expected progress value.
// Uses a small epsilon for floating point comparison.
func AssertProgress(t *testing.T, s store.Store, id string, expected float64) {
	t.Helper()

	node, err := s.GetNode(context.Background(), id)
	require.NoError(t, err, "AssertProgress: failed to get node %s", id)
	assert.InDelta(t, expected, node.Progress, 0.001,
		"AssertProgress: node %s expected progress %f, got %f", id, expected, node.Progress)
}

// AssertNodeExists verifies that a node with the given ID exists in the store.
func AssertNodeExists(t *testing.T, s store.Store, id string) {
	t.Helper()

	_, err := s.GetNode(context.Background(), id)
	require.NoError(t, err, "AssertNodeExists: node %s should exist", id)
}

// AssertNodeNotFound verifies that a node with the given ID does not exist.
func AssertNodeNotFound(t *testing.T, s store.Store, id string) {
	t.Helper()

	_, err := s.GetNode(context.Background(), id)
	assert.ErrorIs(t, err, model.ErrNotFound,
		"AssertNodeNotFound: node %s should not be found", id)
}
