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
	"github.com/hyper-swe/mtix/internal/store"
)

// projectCaptureStore records the NodeFilter passed to ListNodes so the
// multi-project MCP tools (MP-12) can be asserted on the project scope they
// thread into the store. All other store methods come from the embedded
// mcpMockStore.
type projectCaptureStore struct {
	mcpMockStore
	gotFilter store.NodeFilter
}

func (s *projectCaptureStore) ListNodes(_ context.Context, f store.NodeFilter, _ store.ListOptions) ([]*model.Node, int, error) {
	s.gotFilter = f
	return nil, 0, nil
}

// callTool invokes a registered tool and fails the test on a transport error.
func callTool(t *testing.T, reg *ToolRegistry, name, args string) *ToolsCallResult {
	t.Helper()
	res, err := reg.Call(context.Background(), name, json.RawMessage(args))
	require.NoError(t, err)
	require.NotNil(t, res)
	return res
}

// --- MP-12: query tools accept an optional `project` arg --------------------

func TestListTool_ProjectArg_ScopesToProject(t *testing.T) {
	st := &projectCaptureStore{}
	reg := NewToolRegistry()
	registerListTool(reg, st, "MTIX")

	res := callTool(t, reg, "mtix_list", `{"project":"OPS"}`)
	assert.False(t, res.IsError)
	assert.Equal(t, "OPS", st.gotFilter.Project, "an explicit project must scope to exactly that prefix")
}

func TestListTool_ProjectAll_SpansEveryProject(t *testing.T) {
	st := &projectCaptureStore{}
	reg := NewToolRegistry()
	registerListTool(reg, st, "MTIX")

	res := callTool(t, reg, "mtix_list", `{"project":"all"}`)
	assert.False(t, res.IsError)
	assert.Equal(t, "", st.gotFilter.Project, "'all' must clear the project filter so results span every project")
}

func TestListTool_OmittedProject_UsesPrimary(t *testing.T) {
	st := &projectCaptureStore{}
	reg := NewToolRegistry()
	registerListTool(reg, st, "MTIX")

	res := callTool(t, reg, "mtix_list", `{}`)
	assert.False(t, res.IsError)
	assert.Equal(t, "MTIX", st.gotFilter.Project, "an omitted project must default to the configured primary")
}

func TestSearchTool_ProjectArg_ScopesToProject(t *testing.T) {
	st := &projectCaptureStore{}
	reg := NewToolRegistry()
	registerSearchTool(reg, st, "MTIX")

	res := callTool(t, reg, "mtix_search", `{"project":"OPS"}`)
	assert.False(t, res.IsError)
	assert.Equal(t, "OPS", st.gotFilter.Project)
}

func TestSearchTool_ProjectAll_SpansEveryProject(t *testing.T) {
	st := &projectCaptureStore{}
	reg := NewToolRegistry()
	registerSearchTool(reg, st, "MTIX")

	res := callTool(t, reg, "mtix_search", `{"project":"all"}`)
	assert.False(t, res.IsError)
	assert.Equal(t, "", st.gotFilter.Project)
}

func TestSearchTool_OmittedProject_UsesPrimary(t *testing.T) {
	st := &projectCaptureStore{}
	reg := NewToolRegistry()
	registerSearchTool(reg, st, "MTIX")

	res := callTool(t, reg, "mtix_search", `{}`)
	assert.False(t, res.IsError)
	assert.Equal(t, "MTIX", st.gotFilter.Project)
}

func TestBriefingTool_ProjectArg_ScopesToProject(t *testing.T) {
	st := &projectCaptureStore{}
	reg := NewToolRegistry()
	registerBriefingTool(reg, st, "MTIX")

	res := callTool(t, reg, "mtix_briefing", `{"project":"OPS"}`)
	assert.False(t, res.IsError)
	assert.Equal(t, "OPS", st.gotFilter.Project)
}

func TestBriefingTool_ProjectAll_SpansEveryProject(t *testing.T) {
	st := &projectCaptureStore{}
	reg := NewToolRegistry()
	registerBriefingTool(reg, st, "MTIX")

	res := callTool(t, reg, "mtix_briefing", `{"project":"all"}`)
	assert.False(t, res.IsError)
	assert.Equal(t, "", st.gotFilter.Project)
}

func TestBriefingTool_OmittedProject_UsesPrimary(t *testing.T) {
	st := &projectCaptureStore{}
	reg := NewToolRegistry()
	registerBriefingTool(reg, st, "MTIX")

	res := callTool(t, reg, "mtix_briefing", `{}`)
	assert.False(t, res.IsError)
	assert.Equal(t, "MTIX", st.gotFilter.Project)
}

// resolveScopeProject is the shared mapping; assert its contract directly.
func TestResolveScopeProject(t *testing.T) {
	assert.Equal(t, "MTIX", resolveScopeProject("", "MTIX"), "omitted -> primary")
	assert.Equal(t, "", resolveScopeProject("all", "MTIX"), "all -> span every project")
	assert.Equal(t, "OPS", resolveScopeProject("OPS", "MTIX"), "explicit -> that prefix")
	assert.Equal(t, "", resolveScopeProject("", ""), "no primary configured -> span every project")
}

// --- MP-13: mtix_create defaults project to the primary when omitted --------

// createdProject runs mtix_create and returns the project of the created node.
func createdProject(t *testing.T, reg *ToolRegistry, args string) string {
	t.Helper()
	res := callTool(t, reg, "mtix_create", args)
	require.False(t, res.IsError, "create should succeed: %s", res.Content[0].Text)
	var node model.Node
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &node))
	return node.Project
}

func TestCreateTool_OmittedProject_UsesPrimary(t *testing.T) {
	reg := NewToolRegistry()
	registerCreateTool(reg, newTestNodeService(), "MTIX")

	assert.Equal(t, "MTIX", createdProject(t, reg, `{"title":"Default scope"}`),
		"an omitted project must default to the configured primary")
}

func TestCreateTool_WithProject_UsesIt(t *testing.T) {
	reg := NewToolRegistry()
	registerCreateTool(reg, newTestNodeService(), "MTIX")

	assert.Equal(t, "OPS", createdProject(t, reg, `{"title":"Explicit scope","project":"OPS"}`),
		"an explicit project must override the primary")
}

// TestCreateTool_NoLongerRequiresProject guards MP-13: the schema must not
// list `project` as required (only `title`).
func TestCreateTool_NoLongerRequiresProject(t *testing.T) {
	reg := NewToolRegistry()
	registerCreateTool(reg, newTestNodeService(), "MTIX")

	var def *ToolDef
	for i := range reg.List() {
		if reg.List()[i].Name == "mtix_create" {
			def = &reg.List()[i]
			break
		}
	}
	require.NotNil(t, def)
	assert.Equal(t, []string{"title"}, def.InputSchema.Required)
	_, hasProject := def.InputSchema.Properties["project"]
	assert.True(t, hasProject, "project must remain an accepted (optional) argument")
}
