// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// maxInboxWaitSeconds caps mtix_inbox_wait's long-poll. A single MCP tool call
// cannot block indefinitely, so the wait is bounded to a safe ceiling and the
// caller re-invokes on an empty return to keep parking (FR-19.5).
const maxInboxWaitSeconds = 60

// InboxStore is the minimal store surface the inbox tools need. It is satisfied
// by *sqlite.Store; the mcp package depends only on these three methods so the
// per-agent inbox (FR-19.4) reaches the durable event journal without widening
// the general store.Store interface.
type InboxStore interface {
	InboxList(ctx context.Context, agentID string) ([]sqlite.InboxEvent, error)
	InboxWait(ctx context.Context, agentID string, timeout time.Duration) ([]sqlite.InboxEvent, error)
	InboxAck(ctx context.Context, agentID string, seq int64) error
}

// RegisterInboxTools registers the per-agent inbox MCP tools per MTIX-47.6 /
// FR-19.5 — the tool-call mirror of `mtix inbox` so a request-driven agent can
// park on notifications as an ordinary tool call.
func RegisterInboxTools(reg *ToolRegistry, st InboxStore) {
	registerInboxTool(reg, st)
	registerInboxWaitTool(reg, st)
	registerInboxAckTool(reg, st)
}

func registerInboxTool(reg *ToolRegistry, st InboxStore) {
	reg.Register(ToolDef{
		Name:        "mtix_inbox",
		Description: "List comment events addressed to an agent that are past its ack cursor (oldest first). Use mtix_inbox_ack to advance the cursor once handled.",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"agent": {Type: "string", Description: "Agent ID whose inbox to read"},
			},
			Required: []string{"agent"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			Agent string `json:"agent"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse inbox args: %w", err)
		}

		events, err := st.InboxList(ctx, p.Agent)
		if err != nil {
			return nil, err
		}

		data, _ := json.MarshalIndent(events, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerInboxWaitTool(reg *ToolRegistry, st InboxStore) {
	reg.Register(ToolDef{
		Name: "mtix_inbox_wait",
		Description: fmt.Sprintf(
			"Long-poll an agent's inbox: block until an addressed comment lands or the timeout elapses, returning the events (empty JSON array on timeout). "+
				"The wait is capped at %d seconds because a tool call cannot block indefinitely — RE-INVOKE this tool on an empty return to keep parking.",
			maxInboxWaitSeconds),
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"agent":           {Type: "string", Description: "Agent ID whose inbox to wait on"},
				"timeout_seconds": {Type: "number", Description: fmt.Sprintf("Seconds to wait before returning empty (capped at %d)", maxInboxWaitSeconds)},
			},
			Required: []string{"agent"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			Agent          string `json:"agent"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse inbox_wait args: %w", err)
		}

		secs := p.TimeoutSeconds
		if secs <= 0 || secs > maxInboxWaitSeconds {
			secs = maxInboxWaitSeconds
		}

		events, err := st.InboxWait(ctx, p.Agent, time.Duration(secs)*time.Second)
		if err != nil {
			return nil, err
		}
		if events == nil {
			events = []sqlite.InboxEvent{} // marshal an empty [] rather than null on timeout
		}

		data, _ := json.MarshalIndent(events, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerInboxAckTool(reg *ToolRegistry, st InboxStore) {
	reg.Register(ToolDef{
		Name:        "mtix_inbox_ack",
		Description: "Advance an agent's inbox cursor to a seq: every event with seq <= this is marked seen. Idempotent and monotonic (a lower seq never rewinds).",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"agent": {Type: "string", Description: "Agent ID whose cursor to advance"},
				"seq":   {Type: "number", Description: "Sequence watermark to ack up to (from an inbox event's seq)"},
			},
			Required: []string{"agent", "seq"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			Agent string `json:"agent"`
			Seq   int64  `json:"seq"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse inbox_ack args: %w", err)
		}

		if err := st.InboxAck(ctx, p.Agent, p.Seq); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Inbox cursor for %s advanced to %d", p.Agent, p.Seq)), nil
	})
}
