// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests MCP query filters, context/progress results and persistent mutation
// responses through real services and migrated file-backed SQLite fixtures.
package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type behaviorTools struct {
	store   *sqlite.Store
	reg     *ToolRegistry
	nodes   *service.NodeService
	agents  *service.AgentService
	prompts *service.PromptService
}

func newBehaviorTools(t *testing.T) behaviorTools {
	t.Helper()
	s := newInboxTestStore(t)
	now := func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }
	nodes := service.NewNodeService(s, nil, nil, nil, now)
	agents := service.NewAgentService(s, nil, nil, nil, now)
	prompts := service.NewPromptService(s, nil, nil, now)
	sessions := service.NewSessionService(s, nil, nil, now)
	config, err := service.NewConfigService("")
	require.NoError(t, err)
	reg := NewToolRegistry()
	RegisterNodeTools(reg, nodes, WithPrimaryProject("TEST"))
	RegisterWorkflowTools(reg, nodes, service.NewBackgroundService(s, nil, nil, now), WithPrimaryProject("TEST"))
	RegisterContextTools(reg, service.NewContextService(s, nil, nil), prompts)
	RegisterSessionTools(reg, sessions, agents)
	RegisterAnalyticsTools(reg, nodes, agents, config)
	RegisterInboxTools(reg, testInboxService(s))
	RegisterDepTools(reg, service.NewDependencyServiceFromNodeService(nodes))
	return behaviorTools{s, reg, nodes, agents, prompts}
}

func behaviorNode(t *testing.T, env behaviorTools, title, parent string) *model.Node {
	t.Helper()
	n, err := env.nodes.CreateNode(t.Context(), &service.CreateNodeRequest{
		Project: "TEST", Title: title, ParentID: parent, Creator: "test-author",
		Prompt: "Instruction for " + title, Description: "Description for " + title,
	})
	require.NoError(t, err)
	return n
}

func behaviorCall(t *testing.T, reg *ToolRegistry, name, args string) string {
	t.Helper()
	res, err := reg.Call(t.Context(), name, json.RawMessage(args))
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	require.Equal(t, "text", res.Content[0].Type)
	return res.Content[0].Text
}

func seedBehaviorFilters(t *testing.T, env behaviorTools) []*model.Node {
	t.Helper()
	root := behaviorNode(t, env, "Selected subtree", "")
	selected := behaviorNode(t, env, "Worker selected task", root.ID)
	other := behaviorNode(t, env, "Other worker task", root.ID)
	outsider := behaviorNode(t, env, "Unrelated subtree", "")
	require.NoError(t, env.agents.EnsureAgent(t.Context(), "test-worker", "TEST"))
	require.NoError(t, env.agents.EnsureAgent(t.Context(), "test-other", "TEST"))
	require.NoError(t, env.nodes.ClaimNode(t.Context(), selected.ID, "test-worker"))
	require.NoError(t, env.nodes.ClaimNode(t.Context(), other.ID, "test-other"))
	return []*model.Node{root, selected, other, outsider}
}

func TestToolQuery_ListFilters_SelectConcreteNodes(t *testing.T) {
	env := newBehaviorTools(t)
	nodes := seedBehaviorFilters(t, env)
	tests := []struct {
		name, args string
		want       []string
	}{
		{"under", fmt.Sprintf(`{"under":%q}`, nodes[0].ID), []string{nodes[0].ID, nodes[1].ID, nodes[2].ID}},
		{"assignee", `{"assignee":"test-worker"}`, []string{nodes[1].ID}},
		{"combined", fmt.Sprintf(`{"under":%q,"assignee":"test-other"}`, nodes[0].ID), []string{nodes[2].ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got struct {
				Nodes []*model.Node `json:"nodes"`
				Total int           `json:"total"`
			}
			require.NoError(t, json.Unmarshal([]byte(behaviorCall(t, env.reg, "mtix_list", tt.args)), &got))
			ids := make([]string, 0, len(got.Nodes))
			for _, n := range got.Nodes {
				ids = append(ids, n.ID)
				require.NotEmpty(t, n.Title)
			}
			require.ElementsMatch(t, tt.want, ids)
			require.Equal(t, len(tt.want), got.Total)
		})
	}
}

func TestToolQuery_BriefingFilters_ExcludeDistractors(t *testing.T) {
	env := newBehaviorTools(t)
	nodes := seedBehaviorFilters(t, env)
	tests := []struct {
		name, args string
		want       []int
	}{
		{"under", fmt.Sprintf(`{"under":%q}`, nodes[0].ID), []int{0, 1, 2}},
		{"assignee", `{"assignee":" test-worker, , "}`, []int{1}},
		{"type", fmt.Sprintf(`{"type":%q}`, string(nodes[1].NodeType)), []int{1, 2}},
		{"combined", fmt.Sprintf(`{"under":%q,"assignee":"test-other","type":%q}`, nodes[0].ID, string(nodes[2].NodeType)), []int{2}},
		{"empty CSV", `{"under":" , ","assignee":" , ","type":" , "}`, []int{0, 1, 2, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := behaviorCall(t, env.reg, "mtix_briefing", tt.args)
			for i, n := range nodes {
				want := false
				for _, index := range tt.want {
					if i == index {
						want = true
					}
				}
				if want {
					require.Contains(t, out, n.Title)
				} else {
					require.NotContains(t, out, n.Title)
				}
			}
		})
	}
}

func TestToolQuery_ProgressChildren_ReturnExactFields(t *testing.T) {
	env := newBehaviorTools(t)
	root := behaviorNode(t, env, "Progress root", "")
	child := behaviorNode(t, env, "Progress child", root.ID)
	var got struct {
		ID       string  `json:"id"`
		Title    string  `json:"title"`
		Progress float64 `json:"progress"`
		Status   string  `json:"status"`
		Children []struct {
			ID       string  `json:"id"`
			Title    string  `json:"title"`
			Status   string  `json:"status"`
			Progress float64 `json:"progress"`
		} `json:"children"`
	}
	require.NoError(t, json.Unmarshal([]byte(behaviorCall(t, env.reg, "mtix_progress", fmt.Sprintf(`{"id":%q}`, root.ID))), &got))
	require.Equal(t, root.ID, got.ID)
	require.Equal(t, root.Title, got.Title)
	require.Equal(t, "open", got.Status)
	require.Zero(t, got.Progress)
	require.Len(t, got.Children, 1)
	require.Equal(t, child.ID, got.Children[0].ID)
	require.Equal(t, child.Title, got.Children[0].Title)
	require.Equal(t, "open", got.Children[0].Status)
	require.Zero(t, got.Children[0].Progress)
}

func TestToolQuery_Context_ReturnsAncestorAndTargetFields(t *testing.T) {
	env := newBehaviorTools(t)
	root := behaviorNode(t, env, "Context root", "")
	child := behaviorNode(t, env, "Context target", root.ID)
	var got service.ContextResponse
	require.NoError(t, json.Unmarshal([]byte(behaviorCall(t, env.reg, "mtix_context", fmt.Sprintf(`{"id":%q}`, child.ID))), &got))
	require.Len(t, got.Chain, 2)
	for i, n := range []*model.Node{root, child} {
		require.Equal(t, n.ID, got.Chain[i].ID)
		require.Equal(t, n.Title, got.Chain[i].Title)
		require.Equal(t, n.Prompt, got.Chain[i].Prompt)
		require.Equal(t, n.Description, got.Chain[i].Description)
		require.Equal(t, n.Status, got.Chain[i].Status)
	}
	require.Contains(t, got.AssembledPrompt, root.Prompt)
	require.Contains(t, got.AssembledPrompt, child.Prompt)
}

func TestToolQuery_AgentWork_AbsentRefusesAndAssignedReturnsTask(t *testing.T) {
	env := newBehaviorTools(t)
	require.NoError(t, env.agents.EnsureAgent(t.Context(), "test-worker", "TEST"))
	res, err := env.reg.Call(t.Context(), "mtix_agent_work", json.RawMessage(`{"agent_id":"test-worker"}`))
	require.ErrorIs(t, err, model.ErrNotFound)
	require.Nil(t, res, "real service refuses absent work instead of returning nil,nil")
	node := behaviorNode(t, env, "Assigned current task", "")
	require.NoError(t, env.nodes.ClaimNode(t.Context(), node.ID, "test-worker"))
	var got model.Node
	require.NoError(t, json.Unmarshal([]byte(behaviorCall(t, env.reg, "mtix_agent_work", `{"agent_id":"test-worker"}`)), &got))
	require.Equal(t, node.ID, got.ID)
	require.Equal(t, node.Title, got.Title)
	require.Equal(t, "test-worker", got.Assignee)
	require.Equal(t, model.StatusInProgress, got.Status)
}

func TestToolMutation_DescriptionUpdate_PersistsAndPreservesTitle(t *testing.T) {
	env := newBehaviorTools(t)
	node := behaviorNode(t, env, "Description target", "")
	out := behaviorCall(t, env.reg, "mtix_update", fmt.Sprintf(`{"id":%q,"description":"Updated description"}`, node.ID))
	require.Equal(t, "Updated "+node.ID, out)
	after, err := env.store.GetNode(t.Context(), node.ID)
	require.NoError(t, err)
	require.Equal(t, "Updated description", after.Description)
	require.Equal(t, node.Title, after.Title)
	require.Equal(t, node.Status, after.Status)
}

func TestToolMutation_ResolvedAnnotation_PersistsAndLeavesOtherOpen(t *testing.T) {
	env := newBehaviorTools(t)
	node := behaviorNode(t, env, "Annotation target", "")
	for _, text := range []string{"Resolve this instruction", "Keep this instruction"} {
		require.NoError(t, env.prompts.AddAnnotation(t.Context(), node.ID, text, "test-author", ""))
	}
	before, err := env.store.GetNode(t.Context(), node.ID)
	require.NoError(t, err)
	require.Len(t, before.Annotations, 2)
	id := before.Annotations[0].ID
	out := behaviorCall(t, env.reg, "mtix_resolve_annotation", fmt.Sprintf(`{"id":%q,"annotation_id":%q,"author":"test-author"}`, node.ID, id))
	require.Equal(t, "Annotation "+id+" resolved on "+node.ID, out)
	after, err := env.store.GetNode(t.Context(), node.ID)
	require.NoError(t, err)
	require.True(t, after.Annotations[0].Resolved)
	require.False(t, after.Annotations[1].Resolved)
	require.Equal(t, before.Annotations[0].Text, after.Annotations[0].Text)
}

func TestToolMutation_Done_ReturnsSuccessAndPersistsCompletion(t *testing.T) {
	env := newBehaviorTools(t)
	node := behaviorNode(t, env, "Completion target", "")
	require.NoError(t, env.nodes.ClaimNode(t.Context(), node.ID, "test-worker"))
	require.Equal(t, node.ID+" → done", behaviorCall(t, env.reg, "mtix_done", fmt.Sprintf(`{"id":%q}`, node.ID)))
	after, err := env.store.GetNode(t.Context(), node.ID)
	require.NoError(t, err)
	require.Equal(t, model.StatusDone, after.Status)
	require.Equal(t, float64(1), after.Progress)
	require.NotNil(t, after.ClosedAt)
}

func TestToolQuery_SearchFilters_SelectMatchingTask(t *testing.T) {
	env := newBehaviorTools(t)
	nodes := seedBehaviorFilters(t, env)
	args := fmt.Sprintf(`{"query":"task","under":%q,"assignee":"test-worker"}`, nodes[0].ID)
	var got struct {
		Nodes []*model.Node `json:"nodes"`
		Total int           `json:"total"`
	}
	require.NoError(t, json.Unmarshal([]byte(behaviorCall(t, env.reg, "mtix_search", args)), &got))
	require.Len(t, got.Nodes, 1)
	require.Equal(t, nodes[1].ID, got.Nodes[0].ID)
	require.Equal(t, 1, got.Total)
}

func TestToolQuery_ReadOnlyState_DiscoveryAndCallFollowToggle(t *testing.T) {
	env := newBehaviorTools(t)
	node := behaviorNode(t, env, "Read-only task", "")
	require.False(t, env.reg.IsReadOnly())
	env.reg.SetReadOnly(true)
	require.True(t, env.reg.IsReadOnly())
	for _, def := range env.reg.List() {
		require.Equal(t, ScopeRead, def.Scope)
		require.NotEqual(t, "mtix_update", def.Name)
	}
	require.Contains(t, behaviorCall(t, env.reg, "mtix_show", fmt.Sprintf(`{"id":%q}`, node.ID)), node.Title)
	res, err := env.reg.Call(t.Context(), "mtix_update", json.RawMessage(fmt.Sprintf(`{"id":%q,"description":"refused"}`, node.ID)))
	require.ErrorIs(t, err, ErrReadOnly)
	require.Nil(t, res)
	after, err := env.store.GetNode(t.Context(), node.ID)
	require.NoError(t, err)
	require.Equal(t, node.Description, after.Description)
	env.reg.SetReadOnly(false)
	require.False(t, env.reg.IsReadOnly())
	require.Equal(t, "Updated "+node.ID, behaviorCall(t, env.reg, "mtix_update", fmt.Sprintf(`{"id":%q,"description":"allowed"}`, node.ID)))
	after, err = env.store.GetNode(t.Context(), node.ID)
	require.NoError(t, err)
	require.Equal(t, "allowed", after.Description)
}

func TestToolQuery_CSVFilters_TrimAndDiscardEmptyValues(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want []string
	}{{"", nil}, {" , , ", nil}, {" a, ,b, ", []string{"a", "b"}}} {
		t.Run(strings.ReplaceAll(tt.in, ",", "_"), func(t *testing.T) { require.Equal(t, tt.want, splitCSVParam(tt.in)) })
	}
}
