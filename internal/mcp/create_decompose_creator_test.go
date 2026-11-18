// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// claimedParentRegistry builds a registry over a real store holding an
// in_progress parent created by "planner" and claimed by "owner" (FR-11.2a).
func claimedParentRegistry(t *testing.T, opts ...ToolOption) (*ToolRegistry, *sqlite.Store, string) {
	t.Helper()
	st := newInboxTestStore(t)
	svc := service.NewNodeService(st, nil, &service.StaticConfig{AutoClaimEnabled: true}, nil,
		func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) })
	parent, err := svc.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "TEST", Title: "Parent", Creator: "planner"})
	require.NoError(t, err)
	require.NoError(t, st.ClaimNode(context.Background(), parent.ID, "owner"))
	reg := NewToolRegistry()
	RegisterNodeTools(reg, svc, append([]ToolOption{WithPrimaryProject("TEST")}, opts...)...)
	return reg, st, parent.ID
}

func TestDecomposeMCP_CreatorResolution_ClaimsOnlyForParentAssignee(t *testing.T) {
	tests := []struct {
		name         string
		opts         []ToolOption
		creatorArg   string
		wantStatus   model.Status
		wantAssignee string
	}{
		{"server author is the parent assignee", []ToolOption{WithAuthor("owner")}, "", model.StatusInProgress, "owner"},
		{"server author is another agent", []ToolOption{WithAuthor("other")}, "", model.StatusOpen, ""},
		{"no author falls back to mcp", nil, "", model.StatusOpen, ""},
		{"creator argument wins over server author", []ToolOption{WithAuthor("other")}, "owner", model.StatusInProgress, "owner"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, st, parentID := claimedParentRegistry(t, tt.opts...)
			args := map[string]any{"parent_id": parentID, "children": []map[string]string{{"title": "Child"}}}
			if tt.creatorArg != "" {
				args["creator"] = tt.creatorArg
			}
			raw, err := json.Marshal(args)
			require.NoError(t, err)
			_, err = reg.Call(context.Background(), "mtix_decompose", raw)
			require.NoError(t, err)
			got, err := st.GetNode(context.Background(), parentID+".1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantAssignee, got.Assignee)
		})
	}
}

func TestCreateMCP_OmittedCreator_UsesServerAuthor(t *testing.T) {
	tests := []struct {
		name         string
		opts         []ToolOption
		wantCreator  string
		wantAssignee string
	}{
		{"server author owns the parent", []ToolOption{WithAuthor("owner")}, "owner", "owner"},
		{"server author is another agent", []ToolOption{WithAuthor("other")}, "other", ""},
		{"no author falls back to mcp", nil, "mcp", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, _, parentID := claimedParentRegistry(t, tt.opts...)
			raw, err := json.Marshal(map[string]string{"title": "Child", "parent_id": parentID})
			require.NoError(t, err)
			res, err := reg.Call(context.Background(), "mtix_create", raw)
			require.NoError(t, err)
			var n model.Node
			require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &n))
			assert.Equal(t, tt.wantCreator, n.Creator)
			assert.Equal(t, tt.wantAssignee, n.Assignee)
		})
	}
}
