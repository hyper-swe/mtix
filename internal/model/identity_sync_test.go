// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestValidateSyncNodeID_Malformed_ReturnsErrInvalidInput: an id from
// another replica is held to the node id grammar (MTIX-187).
func TestValidateSyncNodeID_Malformed_ReturnsErrInvalidInput(t *testing.T) {
	seg, err := model.RenderUIDSegment(sampleUID)
	require.NoError(t, err)
	tests := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"no_dash", "PRJX"},
		{"lowercase_prefix", "prjx-1"},
		{"non_ascii_segment", "AA-1é.4"},
		{"non_ascii_root", "AA-1é"},
		{"non_ascii_prefix", "ÀA-1"},
		{"prefix_over_20", "ABCDEFGHIJKLMNOPQRSTU-1"},
		{"root_is_uid_segment", "PRJX-" + seg},
		{"trailing_dot", "PRJX-1."},
		{"empty_middle_segment", "PRJX-1..3"},
		{"letter_segment", "PRJX-1.x"},
		{"space_in_segment", "PRJX-1. 2"},
		{"sql_wildcard", "PRJX-1.%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := model.ValidateSyncNodeID(tt.id)
			require.Error(t, err, "id=%q should be rejected", tt.id)
			assert.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

// TestValidateSyncNodeID_Valid_ReturnsNil: settled and provisional ids
// pass, and so does a prefix with an underscore, which the sync prefix
// grammar admits (MTIX-95.17) although a local prefix cannot hold one.
func TestValidateSyncNodeID_Valid_ReturnsNil(t *testing.T) {
	seg, err := model.RenderUIDSegment(sampleUID)
	require.NoError(t, err)
	for _, id := range []string{"PRJX-1", "PRJX-007.3", "MY-PROJ-1.2.3", "PRJX-1." + seg, "DEP_ADD-1.2"} {
		t.Run(id, func(t *testing.T) {
			require.NoError(t, model.ValidateSyncNodeID(id))
		})
	}
	require.Error(t, model.ValidateNodeID("DEP_ADD-1.2"), "the local grammar still refuses '_'")
}
