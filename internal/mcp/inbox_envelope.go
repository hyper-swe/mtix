// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"

	inboxformat "github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func renderInboxEnvelope(agent string, events []service.InboxEvent) (string, error) {
	env, err := oplocal.FromProcess()
	if err != nil {
		return "", fmt.Errorf("MCP inbox operator state: %w", err)
	}
	state := oplocal.New(env)
	caps, reports, err := state.EnvelopeCapsForAgent(agent)
	if err != nil {
		return "", fmt.Errorf("MCP inbox envelope configuration: %w; %s", err, state.EnvelopeCapsRemedy(reports...))
	}
	input := make([]inboxformat.Event, len(events))
	for i, event := range events {
		input[i] = inboxformat.Event{Seq: event.Seq, Task: event.NodeID, Sender: event.Author, Body: event.Body}
	}
	text, err := inboxformat.RenderEnvelope(agent, input, caps)
	if err != nil {
		return "", fmt.Errorf("render MCP inbox envelope: %w", err)
	}
	if len(events) == 0 {
		return "[]", nil
	}
	return text, nil
}

// inboxResult renders a policy-applying read (ADR-007 A12): the first content
// block is the untrusted-message envelope of the eligible events ("[]" when
// none), exactly as before; when the policy held events, a second block carries
// {"events":<n>,"held":{"count":N,"reasons":[...]}} with the count only, never
// held bodies.
func inboxResult(agent string, res service.InboxResult) (*ToolsCallResult, error) {
	text, err := renderInboxEnvelope(agent, res.Events)
	if err != nil {
		return nil, err
	}
	out := SuccessResult(text)
	if res.Held.Count > 0 {
		summary, marshalErr := json.Marshal(struct {
			Events int               `json:"events"`
			Held   service.InboxHeld `json:"held"`
		}{Events: len(res.Events), Held: res.Held})
		if marshalErr != nil {
			return nil, fmt.Errorf("encode held summary: %w", marshalErr)
		}
		out.Content = append(out.Content, TextContent(string(summary)))
	}
	return out, nil
}
