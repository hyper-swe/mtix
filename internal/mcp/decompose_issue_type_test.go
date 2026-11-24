// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestDecomposeMCP_IssueTypeAndUnknownKeys(t *testing.T) {
	reg, st, parentID := claimedParentRegistry(t)
	call := func(children string) error {
		raw, err := json.Marshal(map[string]any{"parent_id": parentID, "children": json.RawMessage(children)})
		require.NoError(t, err)
		_, err = reg.Call(context.Background(), "mtix_decompose", raw)
		return err
	}

	// Unknown key and bad type are refused and nothing is created.
	err1 := call(`[{"title":"a"},{"title":"b","sev":"1"}]`)
	require.ErrorIs(t, err1, model.ErrInvalidInput)
	assert.Contains(t, err1.Error(), "child 1")
	assert.Contains(t, err1.Error(), "sev")
	err2 := call(`[{"title":"a","issue_type":"epic"}]`)
	require.ErrorIs(t, err2, model.ErrInvalidInput)
	assert.Contains(t, err2.Error(), "child 0")
	assert.Contains(t, err2.Error(), "epic")
	kids, err := st.GetDirectChildren(context.Background(), parentID)
	require.NoError(t, err)
	assert.Empty(t, kids)

	require.NoError(t, call(`[{"title":"a","issue_type":"bug"},{"title":"b","type":"feature"},{"title":"c"}]`))
	for i, want := range []model.IssueType{model.IssueTypeBug, model.IssueTypeFeature, ""} {
		n, err := st.GetNode(context.Background(), parentID+"."+string(rune('1'+i)))
		require.NoError(t, err)
		assert.Equal(t, want, n.IssueType)
	}
}
