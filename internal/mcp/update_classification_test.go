// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MCP classification changes have partial update semantics.
package mcp

import (
	"context"
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestUpdateMCP_IssueType_PartialValidated(t *testing.T) {
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", "", "invalid", "omitted"} {
		t.Run(kind, func(t *testing.T) {
			st := newInboxTestStore(t)
			reg := NewToolRegistry()
			svc := service.NewNodeService(st, nil, nil, nil, func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) })
			RegisterNodeTools(reg, svc, WithPrimaryProject("TEST"))
			ctx := context.Background()
			n, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Title: "Classified work", Project: "TEST", IssueType: model.IssueTypeFeature})
			require.NoError(t, err)
			fields := map[string]string{"id": n.ID, "title": "Updated title"}
			if kind != "omitted" {
				fields["issue_type"] = kind
			}
			raw, err := json.Marshal(fields)
			require.NoError(t, err)
			_, err = reg.Call(ctx, "mtix_update", raw)
			if kind == "invalid" {
				require.ErrorIs(t, err, model.ErrInvalidInput)
			} else {
				require.NoError(t, err)
			}
			got, err := st.GetNode(ctx, n.ID)
			require.NoError(t, err)
			want := kind
			if kind == "omitted" || kind == "invalid" {
				want = "feature"
			}
			assert.Equal(t, model.IssueType(want), got.IssueType)
			if kind == "invalid" {
				assert.Equal(t, n.Title, got.Title)
			}
		})
	}
}
func TestUpdateMCP_IssueType_SchemaIncludesClear(t *testing.T) {
	st := newInboxTestStore(t)
	reg := NewToolRegistry()
	svc := service.NewNodeService(st, nil, nil, nil, time.Now)
	RegisterNodeTools(reg, svc)
	for _, def := range reg.List() {
		if def.Name == "mtix_update" {
			assert.Equal(t, []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", ""}, def.InputSchema.Properties["issue_type"].Enum)
			assert.NotContains(t, def.InputSchema.Required, "issue_type")
			return
		}
	}
	t.Fatal("missing update tool")
}
