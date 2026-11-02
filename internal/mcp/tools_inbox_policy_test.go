// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func heldPolicy(reason string) service.InboxPolicy {
	return service.InboxPolicy{Eligibility: func(context.Context, string) (service.InboxEligibility, error) {
		return service.InboxEligibility{Reason: reason}, nil
	}}
}

// TestMCPInbox_ReturnsEventsAndHeldCount: both MCP inbox tools return the
// eligible envelope plus a held summary carrying the count and no held body
// (ADR-007 A12, B1).
func TestMCPInbox_ReturnsEventsAndHeldCount(t *testing.T) {
	s := newInboxTestStore(t)
	inboxSeedNode(t, s, "PROJ-1")
	reg := NewToolRegistry()
	promptSvc := service.NewPromptService(s, nil, slog.Default(), fixedClock)
	RegisterContextTools(reg, newTestContextService(), promptSvc)
	RegisterInboxTools(reg, testInboxService(s).WithInboxPolicy(heldPolicy("unconfirmed")))
	ctx := context.Background()
	_, err := reg.Call(ctx, "mtix_annotate", json.RawMessage(`{"id":"PROJ-1","text":"top secret body","author":"w","to":"opus"}`))
	require.NoError(t, err)

	for _, call := range []struct{ tool, args string }{
		{"mtix_inbox", `{"agent":"opus"}`},
		{"mtix_inbox_wait", `{"agent":"opus","timeout_seconds":1}`},
	} {
		res, err := reg.Call(ctx, call.tool, json.RawMessage(call.args))
		require.NoError(t, err, call.tool)
		require.Len(t, res.Content, 2, call.tool)
		assert.Equal(t, "[]", res.Content[0].Text, call.tool)
		assert.NotContains(t, res.Content[0].Text+res.Content[1].Text, "top secret body", call.tool)
		var held struct {
			Events int               `json:"events"`
			Held   service.InboxHeld `json:"held"`
		}
		require.NoError(t, json.Unmarshal([]byte(res.Content[1].Text), &held), call.tool)
		assert.Equal(t, 0, held.Events)
		assert.Equal(t, service.InboxHeld{Count: 1, Reasons: []string{"unconfirmed"}}, held.Held, call.tool)
	}
}

func TestMCPInbox_NothingHeld_KeepsSingleEnvelopeBlock(t *testing.T) {
	s := newInboxTestStore(t)
	inboxSeedNode(t, s, "PROJ-1")
	reg := NewToolRegistry()
	promptSvc := service.NewPromptService(s, nil, slog.Default(), fixedClock)
	RegisterContextTools(reg, newTestContextService(), promptSvc)
	RegisterInboxTools(reg, testInboxService(s))
	_, err := reg.Call(context.Background(), "mtix_annotate", json.RawMessage(`{"id":"PROJ-1","text":"hello","author":"w","to":"opus"}`))
	require.NoError(t, err)
	res, err := reg.Call(context.Background(), "mtix_inbox", json.RawMessage(`{"agent":"opus"}`))
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	assert.Contains(t, res.Content[0].Text, `body: "hello"`)
}
