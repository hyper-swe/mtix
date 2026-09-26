// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// TestToolRegistryCall_PayloadWarnings_AddedToSuccessfulResultsOnly: with
// payload warnings on, a tool call whose mutation writes a sync event over
// the wire cap gets one warning text block after its own content; with them
// off, unset, or when the call fails or returns an error result, the result
// is left as the handler returned it (MTIX-95.12).
func TestToolRegistryCall_PayloadWarnings_AddedToSuccessfulResultsOnly(t *testing.T) {
	big := strings.Repeat("p", validator.MaxPayloadBytes+100)
	on := func() bool { return true }
	off := func() bool { return false }
	tests := []struct {
		name      string
		enabled   func() bool
		outcome   string // "ok", "error" or "error result"
		wantWarns int
	}{
		{"on, call succeeds", on, "ok", 1},
		{"off", off, "ok", 0},
		{"unset", nil, "ok", 0},
		{"on, call fails", on, "error", 0},
		{"on, call returns an error result", on, "error result", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newInboxTestStore(t)
			inboxSeedNode(t, s, "PROJ-1")
			reg := NewToolRegistry()
			reg.Register(ToolDef{Name: "mtix_test_prompt"}, func(ctx context.Context, _ json.RawMessage) (*ToolsCallResult, error) {
				if err := s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{Prompt: &big}); err != nil {
					return nil, err
				}
				switch tt.outcome {
				case "error":
					return nil, errors.New("tool failed")
				case "error result":
					return ErrorResult("refused"), nil
				}
				return SuccessResult("done"), nil
			})
			reg.SetPayloadWarnings(tt.enabled)

			res, err := reg.Call(context.Background(), "mtix_test_prompt", json.RawMessage(`{}`))
			if tt.outcome == "error" {
				require.Error(t, err)
				require.Nil(t, res)
				return
			}
			require.NoError(t, err)
			require.Len(t, res.Content, 1+tt.wantWarns)
			if tt.wantWarns == 1 {
				require.Equal(t, "done", res.Content[0].Text)
				require.Equal(t, "text", res.Content[1].Type)
				require.True(t, strings.HasPrefix(res.Content[1].Text, "WARN: PROJ-1: the prompt field"), res.Content[1].Text)
				require.Contains(t, res.Content[1].Text, "65536-byte sync limit")
			}
		})
	}
}
