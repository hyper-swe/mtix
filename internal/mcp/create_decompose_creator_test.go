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
// in_progress parent created by "planner" and claimed by the given owner (FR-11.2a).
func claimedParentRegistry(t *testing.T, owner, author string) (*ToolRegistry, *sqlite.Store, string) {
	t.Helper()
	st := newInboxTestStore(t)
	// Use a fixed sync identity so claim tests need no host network interfaces.
	_, seedErr := st.WriteDB().ExecContext(t.Context(), "UPDATE meta SET value = ? WHERE key = ?", "0123456789abcdef", "meta.sync.machine_hash")
	require.NoError(t, seedErr)
	svc := service.NewNodeService(st, nil, &service.StaticConfig{AutoClaimEnabled: true}, nil,
		func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) })
	parent, err := svc.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "TEST", Title: "Parent", Creator: "planner"})
	require.NoError(t, err)
	require.NoError(t, st.ClaimNode(context.Background(), parent.ID, owner))
	reg := NewToolRegistry()
	RegisterAllTools(reg, ToolSet{Node: svc, Primary: "TEST", Author: author})
	return reg, st, parent.ID
}

func TestDecomposeMCP_CreatorResolution_ClaimsOnlyForParentAssignee(t *testing.T) {
	tests := []struct {
		name         string
		owner        string
		serverAuthor string
		creatorArg   string
		wantCreator  string
		wantStatus   model.Status
		wantAssignee string
	}{
		{"server author is the parent assignee", "owner", "owner", "", "owner", model.StatusInProgress, "owner"},
		{"server author is another agent", "owner", "other", "", "other", model.StatusOpen, ""},
		{"no author falls back to mcp", "owner", "", "", "mcp", model.StatusOpen, ""},
		{"fallback equals parent assignee", "mcp", "", "", "mcp", model.StatusOpen, ""},
		{"explicit mcp owns parent", "mcp", "", "mcp", "mcp", model.StatusInProgress, "mcp"},
		{"creator argument wins over server author", "owner", "other", "owner", "owner", model.StatusInProgress, "owner"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, st, parentID := claimedParentRegistry(t, tt.owner, tt.serverAuthor)
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
			assert.Equal(t, tt.wantCreator, got.Creator)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantAssignee, got.Assignee)
		})
	}
}

func TestCreateMCP_OmittedCreator_UsesServerAuthor(t *testing.T) {
	tests := []struct {
		name         string
		owner        string
		serverAuthor string
		wantCreator  string
		wantAssignee string
	}{
		{"server author owns the parent", "owner", "owner", "owner", "owner"},
		{"server author is another agent", "owner", "other", "other", ""},
		{"no author falls back to mcp", "owner", "", "mcp", ""},
		{"fallback equals parent assignee", "mcp", "", "mcp", ""},
		{"configured mcp owns parent", "mcp", "mcp", "mcp", "mcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, _, parentID := claimedParentRegistry(t, tt.owner, tt.serverAuthor)
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
