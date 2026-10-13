// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// maxInboxWaitSeconds is the default and positive-wait ceiling for bounded
// foreground calls. Explicit zero remains unbounded until cancellation (FR-19).
const maxInboxWaitSeconds = 60

// clampInboxWaitSeconds caps positive requests; explicit zero has no limit.
// The handler rejects negative requests before calling this helper (FR-19).
func clampInboxWaitSeconds(secs int) int {
	if secs > maxInboxWaitSeconds {
		return maxInboxWaitSeconds
	}
	return secs
}

// RegisterInboxTools registers list/wait/ack operations through the owning
// service per FR-19.4/FR-19.5. Backends never enter the registry dependency graph.
func RegisterInboxTools(reg *ToolRegistry, svc *service.InboxService) {
	registerInboxTool(reg, svc)
	registerInboxWaitTool(reg, svc)
	registerInboxAckTool(reg, svc)
}

func registerInboxTool(reg *ToolRegistry, svc *service.InboxService) {
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

		events, err := svc.InboxList(ctx, p.Agent)
		if err != nil {
			return nil, err
		}

		data, _ := json.MarshalIndent(events, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerInboxWaitTool(reg *ToolRegistry, svc *service.InboxService) {
	reg.Register(ToolDef{
		Name: "mtix_inbox_wait",
		Description: fmt.Sprintf(
			"Long-poll an agent's inbox: block until an addressed comment lands or the timeout elapses, returning the events (empty JSON array on timeout). "+
				"Positive waits are capped at %d seconds (also the omitted default). Explicit timeout_seconds=0 waits without a limit until an event or caller cancellation. Use bounded foreground waits within harness tool limits; negative timeouts are refused.",
			maxInboxWaitSeconds),
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"agent":           {Type: "string", Description: "Agent ID whose inbox to wait on"},
				"timeout_seconds": {Type: "number", Description: fmt.Sprintf("Seconds to wait: default 60 when omitted, 0 = no limit until cancellation; positive values capped at %d; negative values refused", maxInboxWaitSeconds)},
			},
			Required: []string{"agent"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		return callInboxWait(ctx, args, svc)
	})
}

// callInboxWait preserves explicit zero and caller cancellation (FR-19).
func callInboxWait(ctx context.Context, args json.RawMessage, svc *service.InboxService) (*ToolsCallResult, error) {
	var p struct {
		Agent          string `json:"agent"`
		TimeoutSeconds *int   `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, fmt.Errorf("parse inbox_wait args: %w", err)
	}

	secs := maxInboxWaitSeconds
	if p.TimeoutSeconds != nil {
		if *p.TimeoutSeconds < 0 {
			return nil, fmt.Errorf("inbox_wait: negative timeout_seconds: %w", model.ErrInvalidInput)
		}
		secs = clampInboxWaitSeconds(*p.TimeoutSeconds)
	}

	events, err := svc.InboxWait(ctx, p.Agent, time.Duration(secs)*time.Second)
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = []service.InboxEvent{} // marshal an empty [] rather than null on timeout
	}

	data, _ := json.MarshalIndent(events, "", "  ")
	return SuccessResult(string(data)), nil
}

func registerInboxAckTool(reg *ToolRegistry, svc *service.InboxService) {
	reg.Register(ToolDef{
		Name:        "mtix_inbox_ack",
		Description: "Acknowledge ONE inbox event by its seq (selective): only that event is marked seen, so you can safely process out of order — any event you do not ack reappears on the next mtix_inbox (defer by not acking). Idempotent.",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"agent": {Type: "string", Description: "Agent ID whose inbox to ack"},
				"seq":   {Type: "number", Description: "The seq of the specific inbox event you have handled"},
			},
			Required: []string{"agent", "seq"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			Agent string `json:"agent"`
			Seq   *int64 `json:"seq"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse inbox_ack args: %w", err)
		}
		// Required-arg validation (relay-epic prerequisite fix; FR-21 §12.4):
		// a missing seq used to default to 0 and report SUCCESS while acking
		// nothing — the caller believed the event was handled, the inbox kept
		// resurfacing it, and under a wake-hook setup that meant a wake loop.
		if p.Agent == "" {
			return ErrorResult("inbox_ack: 'agent' is required"), nil
		}
		if p.Seq == nil {
			return ErrorResult("inbox_ack: 'seq' is required — the seq of the specific inbox event you handled (from mtix_inbox)"), nil
		}
		if *p.Seq < 1 {
			return ErrorResult(fmt.Sprintf("inbox_ack: seq %d is not a valid inbox seq (seqs start at 1)", *p.Seq)), nil
		}

		if err := svc.InboxAck(ctx, p.Agent, *p.Seq); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Acked inbox event %d for %s", *p.Seq, p.Agent)), nil
	})
}
