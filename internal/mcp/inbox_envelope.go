// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
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
