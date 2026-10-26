// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Agent identity boundaries reject unsafe nonempty IDs without writes (MTIX-125).
package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func invalidAgentIDs() []struct{ name, id string } {
	return []struct{ name, id string }{
		{"spaces", "   "}, {"unicode_space", "\u2003\u00a0"},
		{"overlong", strings.Repeat("a", 65)}, {"utf8_bytes", strings.Repeat("é", 33)},
		{"nul", "a\x00b"}, {"tab", "a\tb"}, {"newline", "a\nb"},
		{"del", "a\x7fb"}, {"c1", "a\u0085b"},
		{"zero_width", "a\u200bb"}, {"joiner", "a\u200db"},
		{"bidi", "a\u202eb"}, {"bidi_isolate", "a\u2066b"},
		{"bom", "a\ufeffb"}, {"soft_hyphen", "a\u00adb"},
	}
}

func TestAgentID_MCPBoundaries_RejectWithoutMutation(t *testing.T) {
	for _, tool := range []string{"mtix_create", "mtix_claim"} {
		t.Run(tool, func(t *testing.T) {
			for _, tc := range invalidAgentIDs() {
				t.Run(tc.name, func(t *testing.T) {
					st := newInboxTestStore(t)
					ctx := context.Background()
					inboxSeedNode(t, st, "PROJ-1")
					bus := &mutationEvents{}
					svc := service.NewNodeService(st, bus, nil, nil, fixedClock)
					reg := NewToolRegistry()
					RegisterNodeTools(reg, svc, WithPrimaryProject("PROJ"))
					RegisterWorkflowTools(reg, svc, service.NewBackgroundService(st, nil, nil, fixedClock))
					args := map[string]string{"id": "PROJ-1", "agent_id": tc.id}
					if tool == "mtix_create" {
						args = map[string]string{"title": "refused", "assignee": tc.id}
					}
					before, err := st.GetNode(ctx, "PROJ-1")
					require.NoError(t, err)
					raw, err := json.Marshal(args)
					require.NoError(t, err)
					_, err = reg.Call(ctx, tool, raw)
					require.ErrorIs(t, err, model.ErrInvalidInput)
					require.Contains(t, err.Error(), "agent ID")
					require.Empty(t, bus.events)
					after, err := st.GetNode(ctx, "PROJ-1")
					require.NoError(t, err)
					require.Equal(t, before, after)
				})
			}
		})
	}
}

func TestAgentID_MCPValidRawAndEmptyCreate_Preserved(t *testing.T) {
	for _, agent := range []string{"", " Agent.User+é ", strings.Repeat("a", 64), strings.Repeat("é", 32)} {
		t.Run(agent, func(t *testing.T) {
			st := newInboxTestStore(t)
			svc := service.NewNodeService(st, nil, nil, nil, fixedClock)
			reg := NewToolRegistry()
			RegisterNodeTools(reg, svc, WithPrimaryProject("PROJ"))
			RegisterWorkflowTools(reg, svc, service.NewBackgroundService(st, nil, nil, fixedClock))
			raw, err := json.Marshal(map[string]string{"title": "raw", "assignee": agent})
			require.NoError(t, err)
			result, err := reg.Call(context.Background(), "mtix_create", raw)
			require.NoError(t, err)
			var n model.Node
			require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &n))
			require.Equal(t, agent, n.Assignee)
			if agent != "" {
				raw, err = json.Marshal(map[string]string{"id": n.ID, "reason": "release"})
				require.NoError(t, err)
				_, err = reg.Call(context.Background(), "mtix_unclaim", raw)
				require.NoError(t, err)
				raw, err = json.Marshal(map[string]string{"id": n.ID, "agent_id": agent})
				require.NoError(t, err)
				_, err = reg.Call(context.Background(), "mtix_claim", raw)
				require.NoError(t, err)
				got, err := st.GetNode(context.Background(), n.ID)
				require.NoError(t, err)
				require.Equal(t, agent, got.Assignee)
			}
		})
	}
}

func TestAgentID_MCPSchema_ExplainsValidationAndEmptyAssignment(t *testing.T) {
	st := newInboxTestStore(t)
	svc := service.NewNodeService(st, nil, nil, nil, fixedClock)
	reg := NewToolRegistry()
	RegisterNodeTools(reg, svc)
	RegisterWorkflowTools(reg, svc, service.NewBackgroundService(st, nil, nil, fixedClock))
	for _, def := range reg.List() {
		if def.Name == "mtix_create" || def.Name == "mtix_claim" {
			key := "agent_id"
			if def.Name == "mtix_create" {
				key = "assignee"
			}
			text := def.InputSchema.Properties[key].Description
			for _, phrase := range []string{"64 UTF-8 bytes", "whitespace-only", "control", "format", "raw"} {
				require.Contains(t, text, phrase)
			}
			if key == "assignee" {
				require.Contains(t, text, "empty")
			}
		}
	}
}
