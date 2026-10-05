// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MCP creation keeps optional creator independent of issue type and assignee.
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
)

func TestCreateMCP_ClassificationAndAssignment(t *testing.T) {
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", "", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			st := newInboxTestStore(t)
			reg := NewToolRegistry()
			svc := service.NewNodeService(st, nil, nil, nil, func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) })
			RegisterNodeTools(reg, svc, st, WithPrimaryProject("TEST"))
			raw, err := json.Marshal(map[string]string{"title": "Classified work", "issue_type": kind, "assignee": "worker", "creator": "author"})
			require.NoError(t, err)
			result, err := reg.Call(context.Background(), "mtix_create", raw)
			if kind == "invalid" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "refactor")
				return
			}
			require.NoError(t, err)
			require.False(t, result.IsError)
			var n model.Node
			require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &n))
			assert.Equal(t, model.IssueType(kind), n.IssueType)
			assert.Equal(t, model.NodeTypeEpic, n.NodeType)
			assert.Equal(t, "worker", n.Assignee)
			assert.Equal(t, "author", n.Creator)
			assert.Equal(t, model.StatusInProgress, n.Status)
			got, err := st.GetNode(context.Background(), n.ID)
			require.NoError(t, err)
			assert.Equal(t, n.IssueType, got.IssueType)
			assert.Equal(t, n.Assignee, got.Assignee)
		})
	}
}

func TestCreateMCP_SchemaAndOmittedAuthor(t *testing.T) {
	st := newInboxTestStore(t)
	reg := NewToolRegistry()
	svc := service.NewNodeService(st, nil, nil, nil, func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) })
	RegisterNodeTools(reg, svc, st, WithPrimaryProject("TEST"))
	var schema SchemaObj
	for _, def := range reg.List() {
		if def.Name == "mtix_create" {
			schema = def.InputSchema
		}
	}
	assert.Equal(t, []string{"bug", "feature", "task", "chore", "refactor", "test", "doc"}, schema.Properties["issue_type"].Enum)
	assert.Contains(t, schema.Properties, "assignee")
	assert.Contains(t, schema.Properties, "creator")
	assert.NotContains(t, schema.Required, "issue_type")
	assert.NotContains(t, schema.Required, "assignee")
	result, err := reg.Call(context.Background(), "mtix_create", json.RawMessage(`{"title":"Unassigned default"}`))
	require.NoError(t, err)
	var n model.Node
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &n))
	assert.Empty(t, n.IssueType)
	assert.Empty(t, n.Assignee)
	assert.Equal(t, "mcp", n.Creator)
	assert.Equal(t, model.StatusOpen, n.Status)
}
