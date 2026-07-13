// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package channel

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClaudeCode_Push_BuildsNotificationFrame: Push emits the documented
// Claude Code channel notification — method notifications/claude/channel with
// the event body as content and identifier-keyed routing meta (MTIX-56.7).
func TestClaudeCode_Push_BuildsNotificationFrame(t *testing.T) {
	var gotMethod string
	var gotParams ClaudeParams
	ad := NewClaudeCode(func(method string, params any) error {
		gotMethod = method
		gotParams = params.(ClaudeParams)
		return nil
	})

	assert.Equal(t, "claude-code", ad.Name())
	require.NoError(t, ad.Push(Event{Seq: 41, Node: "PROJ-7", From: "planner", Body: "start on PROJ-7"}))

	assert.Equal(t, ClaudeNotificationMethod, gotMethod)
	assert.Equal(t, "start on PROJ-7", gotParams.Content, "the comment body is the channel content")
	assert.Equal(t, map[string]string{"from": "planner", "node": "PROJ-7", "seq": "41"}, gotParams.Meta,
		"routing facts ride as identifier-keyed meta")
}

// TestClaudeCode_Push_PropagatesNotifyError: a send failure surfaces so the
// pump can log it (the event stays durably in the inbox).
func TestClaudeCode_Push_PropagatesNotifyError(t *testing.T) {
	ad := NewClaudeCode(func(string, any) error { return errors.New("session gone") })
	require.Error(t, ad.Push(Event{Seq: 1, Node: "P-1", From: "a", Body: "x"}))
}

// TestClaudeInstructions_TeachesTheLoop: the system-prompt string names the
// agent and the exact tools that close the handle→ack→reply loop, so a live
// agent knows what to do with a pushed event without extra prompting.
func TestClaudeInstructions_TeachesTheLoop(t *testing.T) {
	got := ClaudeInstructions("developer")
	assert.Contains(t, got, `agent "developer"`)
	assert.Contains(t, got, `source="mtix"`, "explains the channel tag it will see")
	assert.Contains(t, got, "mtix_inbox_ack", "the ack step")
	assert.Contains(t, got, "mtix_annotate", "the reply step")
	assert.Contains(t, got, "mtix_show", "the inspect step")
}
