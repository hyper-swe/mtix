// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func searchTitles(t *testing.T, env behaviorTools, args string) []string {
	t.Helper()
	var got struct {
		Nodes []*model.Node `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal([]byte(behaviorCall(t, env.reg, "mtix_search", args)), &got))
	titles := []string{}
	for _, n := range got.Nodes {
		titles = append(titles, n.Title)
	}
	return titles
}

// MTIX-146: mtix_search applies its query exactly as the CLI does.
func TestToolQuery_SearchQuery_FiltersRows(t *testing.T) {
	env := newBehaviorTools(t)
	behaviorNode(t, env, "Alpha template", "")
	behaviorNode(t, env, "Beta", "")
	assert.Equal(t, []string{"Alpha template"}, searchTitles(t, env, `{"query":"TEMPLATE"}`))
	assert.Empty(t, searchTitles(t, env, `{"query":"xyzzynomatch"}`))
	assert.Empty(t, searchTitles(t, env, `{"query":"alpha beta"}`), "all words must match")
	assert.Len(t, searchTitles(t, env, `{}`), 2)
}

func TestToolQuery_SearchQuery_PercentIsLiteral(t *testing.T) {
	env := newBehaviorTools(t)
	behaviorNode(t, env, "Alpha template", "")
	assert.Empty(t, searchTitles(t, env, `{"query":"%"}`))
}

// MTIX-146: unknown or malformed arguments are an error, not "return everything".
func TestToolQuery_SearchBadArguments_ReturnError(t *testing.T) {
	env := newBehaviorTools(t)
	behaviorNode(t, env, "Alpha template", "")
	for name, args := range map[string]string{
		"unknown key":    `{"text":"template"}`,
		"wrong type":     `{"query":5}`,
		"malformed json": `{"query":`,
		"NUL in query":   `{"query":"a\u0000b"}`,
	} {
		t.Run(name, func(t *testing.T) {
			res, err := env.reg.Call(t.Context(), "mtix_search", json.RawMessage(args))
			if err == nil {
				require.NotNil(t, res)
				assert.True(t, res.IsError, "must not return rows for %s", args)
			}
		})
	}
}
