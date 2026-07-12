// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package channel

import (
	"fmt"
	"strconv"
)

// Claude Code channels adapter (research preview). EVERYTHING protocol-
// specific lives in this file, pinned by the stdio contract test in
// internal/mcp — when Anthropic revises the research-preview contract, this
// file and that test are the whole blast radius.
//
// Contract (code.claude.com/docs/en/channels-reference, v2.1.80+):
//   - the server declares capabilities.experimental["claude/channel"] = {}
//     at initialize, which makes Claude Code register a notification listener;
//   - each pushed event is the JSON-RPC notification method
//     "notifications/claude/channel" with params {content, meta}; meta keys
//     must be bare identifiers (letters, digits, underscores) — others are
//     silently dropped by the client;
//   - events reach the session only while it runs, queued while it is busy.
//     Cold-start stays the exec rung's job.

const (
	// ClaudeCapabilityKey is the experimental capability whose presence makes
	// Claude Code treat this MCP server as a channel.
	ClaudeCapabilityKey = "claude/channel"

	// ClaudeNotificationMethod is the JSON-RPC method carrying one event.
	ClaudeNotificationMethod = "notifications/claude/channel"
)

// ClaudeNotifyFunc sends one JSON-RPC notification to the connected client —
// satisfied by (*mcp.Server).SendNotification.
type ClaudeNotifyFunc func(method string, params any) error

// ClaudeParams is the notification payload shape Claude Code expects.
type ClaudeParams struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// ClaudeCode pushes inbox events into a running Claude Code session as
// channel notifications. Implements Adapter.
type ClaudeCode struct {
	notify ClaudeNotifyFunc
}

// NewClaudeCode wraps a notification sender as a channel adapter.
func NewClaudeCode(notify ClaudeNotifyFunc) *ClaudeCode {
	return &ClaudeCode{notify: notify}
}

func (c *ClaudeCode) Name() string { return "claude-code" }

// Push implements Adapter: the event body is the <channel> tag content, the
// routing facts ride as tag attributes (identifier-keyed meta).
func (c *ClaudeCode) Push(e Event) error {
	return c.notify(ClaudeNotificationMethod, ClaudeParams{
		Content: e.Body,
		Meta: map[string]string{
			"from": e.From,
			"node": e.Node,
			"seq":  strconv.FormatInt(e.Seq, 10),
		},
	})
}

// ClaudeInstructions is the system-prompt text announced at initialize: it
// tells Claude what the events are and how to close the loop with the mtix
// tools this same server already exposes (handle → ack → reply). Two-way for
// free — no separate reply tool needed.
func ClaudeInstructions(agent string) string {
	return fmt.Sprintf(`mtix inbox events for agent %q arrive as <channel source="mtix" from="..." node="..." seq="...">. `+
		`Each is a comment addressed to you on the named task. Handle it: inspect the task with mtix_show/mtix_context, `+
		`do the work, then acknowledge with mtix_inbox_ack (the seq attribute) so it does not resurface. `+
		`Reply to the sender with mtix_annotate on the node, addressed via 'to'. `+
		`Only acknowledge events you have actually handled.`, agent)
}
