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

// An addressed mtix_annotate requests the push after its commit and reports
// the state; an ordinary annotation never does (ADR-007 D21, MTIX-112.20.2).
func TestAnnotateTool_AddressedSend_RequestsPush(t *testing.T) {
	tests := []struct {
		name      string
		args      string
		wantCalls []string
		wantText  string
	}{
		{"addressed", `{"id":"TEST-1","text":"n","author":"a","to":"agent-b"}`, []string{"agent-b"}, "push: queued"},
		{"ordinary", `{"id":"TEST-1","text":"n","author":"a"}`, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			reg := NewToolRegistry()
			RegisterContextTools(reg, newTestContextService(), newTestPromptService(),
				WithAddressedSendHook(func(_ context.Context, to string) string {
					calls = append(calls, to)
					return "queued"
				}))
			res, err := reg.Call(context.Background(), "mtix_annotate", json.RawMessage(tt.args))
			require.NoError(t, err)
			assert.Equal(t, tt.wantCalls, calls)
			assert.Contains(t, res.Content[0].Text, tt.wantText)
		})
	}
}

func TestAnnotateTool_AddressedSendRefused_NoHookCall(t *testing.T) {
	called := false
	reg := NewToolRegistry()
	RegisterContextTools(reg, newTestContextService(),
		service.NewPromptService(newInboxTestStore(t), nil, slog.Default(), fixedClock),
		WithAddressedSendHook(func(context.Context, string) string { called = true; return "queued" }))
	// An unknown node id makes the commit fail: the hook must not run.
	_, err := reg.Call(context.Background(), "mtix_annotate",
		json.RawMessage(`{"id":"NO-SUCH-1","text":"n","author":"a","to":"agent-b"}`))
	require.Error(t, err, "the annotation must be refused")
	assert.False(t, called, "a failed commit requests no push")
}
