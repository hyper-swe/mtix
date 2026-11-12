// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func labelledNode(t *testing.T, env behaviorTools, title string, labels ...string) {
	t.Helper()
	_, err := env.nodes.CreateNode(t.Context(), &service.CreateNodeRequest{
		Project: "TEST", Title: title, Creator: "test-author", Labels: labels,
		Prompt: "p " + title, Description: "d " + title,
	})
	require.NoError(t, err)
}

func toolTitles(t *testing.T, env behaviorTools, tool, args string) []string {
	t.Helper()
	var got struct {
		Nodes []*model.Node `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal([]byte(behaviorCall(t, env.reg, tool, args)), &got))
	titles := []string{}
	for _, n := range got.Nodes {
		titles = append(titles, n.Title)
	}
	return titles
}

// MTIX-255.4: mtix_list and mtix_search take a comma-separated any-of labels filter.
func TestToolQuery_LabelsFilter_ListAndSearchAnyOf(t *testing.T) {
	env := newBehaviorTools(t)
	labelledNode(t, env, "Now one", "desk:now")
	labelledNode(t, env, "Backlog one", "desk:backlog", "extra")
	labelledNode(t, env, "Unlabelled")
	for _, tool := range []string{"mtix_list", "mtix_search"} {
		t.Run(tool, func(t *testing.T) {
			assert.Equal(t, []string{"Now one"}, toolTitles(t, env, tool, `{"labels":"desk:now"}`))
			assert.ElementsMatch(t, []string{"Now one", "Backlog one"},
				toolTitles(t, env, tool, `{"labels":"desk:now, desk:backlog"}`))
			assert.Empty(t, toolTitles(t, env, tool, `{"labels":"missing"}`))
			assert.Len(t, toolTitles(t, env, tool, `{"labels":" , "}`), 3, "blank entries mean no filter")
			assert.Len(t, toolTitles(t, env, tool, `{}`), 3)
			assert.ElementsMatch(t, []string{"Now one", "Backlog one"},
				toolTitles(t, env, tool, `{"labels":"desk:backlog,desk:now","status":"open"}`))
			assert.Empty(t, toolTitles(t, env, tool, `{"labels":"desk:now","status":"done"}`))
		})
	}
}

// MTIX-255.4: labels combines with the text query in mtix_search.
func TestToolQuery_LabelsFilter_SearchCombinesWithQuery(t *testing.T) {
	env := newBehaviorTools(t)
	labelledNode(t, env, "Alpha thing", "desk:now")
	labelledNode(t, env, "Beta thing", "desk:now")
	assert.Equal(t, []string{"Alpha thing"},
		toolTitles(t, env, "mtix_search", `{"query":"alpha","labels":"desk:now"}`))
	assert.Empty(t, toolTitles(t, env, "mtix_search", `{"query":"alpha","labels":"desk:backlog"}`))
}
