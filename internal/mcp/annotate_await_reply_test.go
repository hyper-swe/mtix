// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

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

// mtix_annotate with await_reply opens the sender window through the hook and
// reports it; every spelling of the duration follows the CLI rules
// (default 1h, at most 8h) (MTIX-112.20.3).
func TestAnnotateTool_AwaitReply_Table_OpensWindowThroughHook(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		wantWait time.Duration
		wantErr  bool
	}{
		{"explicit", `{"id":"TEST-1","text":"n","author":"a","to":"agent-b","await_reply":"30m"}`, 30 * time.Minute, false},
		{"default keyword", `{"id":"TEST-1","text":"n","author":"a","to":"agent-b","await_reply":"default"}`, time.Hour, false},
		{"clamped", `{"id":"TEST-1","text":"n","author":"a","to":"agent-b","await_reply":"99h"}`, 8 * time.Hour, false},
		{"bad duration", `{"id":"TEST-1","text":"n","author":"a","to":"agent-b","await_reply":"soon"}`, 0, true},
		{"no addressee", `{"id":"TEST-1","text":"n","author":"a","await_reply":"30m"}`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []service.AnnotationResult
			reg := NewToolRegistry()
			RegisterContextTools(reg, newTestContextService(), newTestPromptService(),
				WithReplyWindowHook(func(_ context.Context, node, author string, res service.AnnotationResult) string {
					assert.Equal(t, "TEST-1", node)
					assert.Equal(t, "a", author)
					got = append(got, res)
					return ""
				}))
			res, err := reg.Call(context.Background(), "mtix_annotate", json.RawMessage(tt.args))
			if tt.wantErr {
				require.Error(t, err)
				assert.Empty(t, got, "a refused send opens no window")
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, "agent-b", got[0].Addressee)
			assert.WithinDuration(t, fixedClock().Add(tt.wantWait), got[0].ReplyBy, time.Second)
			assert.Contains(t, res.Content[0].Text, "reply window open until")
		})
	}
}

func TestAnnotateTool_AwaitReply_HookWarning_Surfaces(t *testing.T) {
	reg := NewToolRegistry()
	RegisterContextTools(reg, newTestContextService(), newTestPromptService(),
		WithReplyWindowHook(func(context.Context, string, string, service.AnnotationResult) string { return "state unwritable" }))
	res, err := reg.Call(context.Background(), "mtix_annotate",
		json.RawMessage(`{"id":"TEST-1","text":"n","author":"a","to":"agent-b","await_reply":"10m"}`))
	require.NoError(t, err, "the comment was sent; a window failure is a warning")
	assert.Contains(t, res.Content[0].Text, "reply window: state unwritable")
}

func TestAnnotateTool_NoAwaitReply_NeverCallsHook(t *testing.T) {
	called := false
	reg := NewToolRegistry()
	RegisterContextTools(reg, newTestContextService(), newTestPromptService(),
		WithReplyWindowHook(func(context.Context, string, string, service.AnnotationResult) string { called = true; return "" }))
	_, err := reg.Call(context.Background(), "mtix_annotate",
		json.RawMessage(`{"id":"TEST-1","text":"n","author":"a","to":"agent-b"}`))
	require.NoError(t, err)
	assert.False(t, called, "scale-to-zero: no await_reply, no window")
	assert.Equal(t, model.DefaultReplyWindow, time.Hour)
}

func TestAnnotateTool_AwaitReply_BlankAuthor_RefusedWithoutWindow(t *testing.T) {
	tests := []struct {
		name string
		args string
	}{
		{"empty author", `{"id":"TEST-1","text":"n","author":"","to":"agent-b","await_reply":"30m"}`},
		{"whitespace author", `{"id":"TEST-1","text":"n","author":"  ","to":"agent-b","await_reply":"30m"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			reg := NewToolRegistry()
			RegisterContextTools(reg, newTestContextService(), newTestPromptService(),
				WithReplyWindowHook(func(context.Context, string, string, service.AnnotationResult) string { called = true; return "" }))
			_, err := reg.Call(context.Background(), "mtix_annotate", json.RawMessage(tt.args))
			require.ErrorIs(t, err, model.ErrInvalidInput)
			assert.Contains(t, err.Error(), "author", "the error names the field to fix")
			assert.False(t, called, "a refused send opens no window")
		})
	}
}
