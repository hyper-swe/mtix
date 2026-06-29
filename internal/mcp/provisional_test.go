// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestShowResultJSON_SettledID_NoProvisionalFlag verifies a settled node's show
// payload carries no provisional flag and preserves the existing JSON shape —
// CORNER: settled id shows no marker, non-breaking (ADR-003 §8).
func TestShowResultJSON_SettledID_NoProvisionalFlag(t *testing.T) {
	out := showResultJSON(&model.Node{ID: "PROJ-1.4", Title: "settled"})

	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	assert.Equal(t, "PROJ-1.4", m["id"], "existing fields must be preserved")
	_, hasFlag := m["provisional"]
	assert.False(t, hasFlag, "settled node must not carry a provisional flag")
}

// TestShowResultJSON_ProvisionalID_Flagged verifies a provisional node's show
// payload sets provisional=true while preserving existing fields — CORNER:
// single-level provisional flagged (ADR-003 §8).
func TestShowResultJSON_ProvisionalID_Flagged(t *testing.T) {
	out := showResultJSON(&model.Node{ID: "PROJ-1.u0a1b2c3d4e5", Title: "prov"})

	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	assert.Equal(t, "PROJ-1.u0a1b2c3d4e5", m["id"], "existing fields must be preserved")
	assert.Equal(t, true, m["provisional"], "provisional node must be flagged")
}

// TestShowResultJSON_DeeplyNestedProvisional_Flagged verifies a deeply-nested
// provisional id is flagged — EDGE: deeply-nested provisional (ADR-003 §8).
func TestShowResultJSON_DeeplyNestedProvisional_Flagged(t *testing.T) {
	out := showResultJSON(&model.Node{ID: "PROJ-1.2.u0a1b2c3d4e5.4", Title: "deep"})

	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	assert.Equal(t, true, m["provisional"])
}
