// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// White-box table for cascadeProgressTargets (MTIX-95.21): which ancestors a
// cascade cancel recomputes, and in which order. The black-box tests in
// cancel_cascade_test.go cover the store-level effect.
package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCascadeProgressTargets_CancelledDescendants_ReturnsSubtreeAncestorsDeepestFirst
// pins the target set (every ancestor of a cancelled node up to and
// including the root, each once, nothing outside the subtree) and its order
// (deepest first, ties by id).
func TestCascadeProgressTargets_CancelledDescendants_ReturnsSubtreeAncestorsDeepestFirst(t *testing.T) {
	tests := []struct {
		name      string
		root      string
		cancelled []cancelledNode
		want      []string
	}{
		{"nothing cancelled", "P-1", nil, nil},
		{"direct children share the root", "P-1",
			[]cancelledNode{{"P-1.1", "P-1"}, {"P-1.2", "P-1"}},
			[]string{"P-1"}},
		{"shared ancestors appear once, deepest first", "P-1",
			[]cancelledNode{
				{"P-1.2.1", "P-1.2"}, {"P-1.1.1.1", "P-1.1.1"}, {"P-1.1", "P-1"},
				{"P-1.1.1", "P-1.1"}, {"P-1.1.1.2", "P-1.1.1"}, {"P-1.2", "P-1"},
			},
			[]string{"P-1.1.1", "P-1.1", "P-1.2", "P-1"}},
		{"an ancestor that was not cancelled is walked through", "P-1",
			[]cancelledNode{{"P-1.3.1", "P-1.3"}},
			[]string{"P-1.3", "P-1"}},
		{"a root below the top stops at the root", "P-1.2",
			[]cancelledNode{{"P-1.2.1.1", "P-1.2.1"}},
			[]string{"P-1.2.1", "P-1.2"}},
		{"a look-alike id sharing the root's text is not in the subtree", "P-1",
			[]cancelledNode{{"P-10.1", "P-10"}},
			nil},
		{"a parent outside the subtree is never returned", "P-1",
			[]cancelledNode{{"P-1.1", "Q-9"}},
			nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cascadeProgressTargets(tt.root, tt.cancelled))
		})
	}
}
