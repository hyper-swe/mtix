// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProgressTool_ExclusionMetadata_Present covers FR-5.6a and FR-5.6b on MCP.
func TestProgressTool_ExclusionMetadata_Present(t *testing.T) {
	env := newBehaviorTools(t)
	root := behaviorNode(t, env, "root", "")
	a := behaviorNode(t, env, "a", root.ID)
	b := behaviorNode(t, env, "b", root.ID)
	grand := behaviorNode(t, env, "grand", b.ID)
	require.NoError(t, env.store.TransitionStatus(t.Context(), grand.ID, model.StatusInvalidated, "r", "t"))
	require.NoError(t, env.store.TransitionStatus(t.Context(), a.ID, model.StatusCancelled, "d", "t"))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(behaviorCall(t, env.reg, "mtix_progress", jsonArgs(t, "id", root.ID))), &got))
	assert.Equal(t, float64(1), got["invalidated_count"])
	assert.Equal(t, true, got["has_invalidated_descendants"])
	assert.Equal(t, false, got["all_children_excluded"])

	require.NoError(t, env.store.TransitionStatus(t.Context(), b.ID, model.StatusInvalidated, "r", "t"))
	require.NoError(t, json.Unmarshal([]byte(behaviorCall(t, env.reg, "mtix_progress", jsonArgs(t, "id", root.ID))), &got))
	assert.Equal(t, true, got["all_children_excluded"])
}
