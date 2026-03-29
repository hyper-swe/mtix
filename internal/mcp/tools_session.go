// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// RegisterSessionTools registers session and agent MCP tools per MTIX-6.2.5.
func RegisterSessionTools(reg *ToolRegistry, sessionSvc *service.SessionService, agentSvc *service.AgentService) {
	registerSessionStartTool(reg, sessionSvc)
	registerSessionEndTool(reg, sessionSvc)
	registerSessionSummaryTool(reg, sessionSvc)
	registerAgentHeartbeatTool(reg, agentSvc)
	registerAgentStateTool(reg, agentSvc)
	registerAgentWorkTool(reg, agentSvc)
}

func registerSessionStartTool(reg *ToolRegistry, svc *service.SessionService) {
	reg.Register(ToolDef{
		Name:        "mtix_session_start",
		Description: "Start a new agent session",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"agent_id": {Type: "string", Description: "Agent ID"},
				"project":  {Type: "string", Description: "Project prefix"},
			},
			Required: []string{"agent_id", "project"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			AgentID string `json:"agent_id"`
			Project string `json:"project"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse session_start args: %w", err)
		}

		sessionID, err := svc.SessionStart(ctx, p.AgentID, p.Project)
		if err != nil {
			return nil, err
		}

		data, _ := json.MarshalIndent(map[string]string{
			"session_id": sessionID,
			"agent_id":   p.AgentID,
		}, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerSessionEndTool(reg *ToolRegistry, svc *service.SessionService) {
	reg.Register(ToolDef{
		Name:        "mtix_session_end",
		Description: "End the current agent session",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"agent_id": {Type: "string", Description: "Agent ID"},
			},
			Required: []string{"agent_id"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct{ AgentID string `json:"agent_id"` }
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse session_end args: %w", err)
		}

		if err := svc.SessionEnd(ctx, p.AgentID); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Session ended for agent %s", p.AgentID)), nil
	})
}

func registerSessionSummaryTool(reg *ToolRegistry, svc *service.SessionService) {
	reg.Register(ToolDef{
		Name:        "mtix_session_summary",
		Description: "Get summary of the current agent session",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"agent_id": {Type: "string", Description: "Agent ID"},
			},
			Required: []string{"agent_id"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct{ AgentID string `json:"agent_id"` }
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse session_summary args: %w", err)
		}

		summary, err := svc.SessionSummary(ctx, p.AgentID)
		if err != nil {
			return nil, err
		}

		data, _ := json.MarshalIndent(summary, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerAgentHeartbeatTool(reg *ToolRegistry, svc *service.AgentService) {
	reg.Register(ToolDef{
		Name:        "mtix_agent_heartbeat",
		Description: "Send agent heartbeat to prevent stale detection",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"agent_id": {Type: "string", Description: "Agent ID"},
			},
			Required: []string{"agent_id"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct{ AgentID string `json:"agent_id"` }
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse heartbeat args: %w", err)
		}

		if err := svc.Heartbeat(ctx, p.AgentID); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Heartbeat recorded for %s", p.AgentID)), nil
	})
}

func registerAgentStateTool(reg *ToolRegistry, svc *service.AgentService) {
	reg.Register(ToolDef{
		Name:        "mtix_agent_state",
		Description: "Get or set an agent's state",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"agent_id": {Type: "string", Description: "Agent ID"},
				"state":    {Type: "string", Description: "New state (omit to get current): idle, working, stuck, done"},
			},
			Required: []string{"agent_id"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			AgentID string `json:"agent_id"`
			State   string `json:"state"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse agent_state args: %w", err)
		}

		// If no state provided, return current state.
		if p.State == "" {
			state, err := svc.GetAgentState(ctx, p.AgentID)
			if err != nil {
				return nil, err
			}
			data, _ := json.MarshalIndent(map[string]string{
				"agent_id": p.AgentID,
				"state":    string(state),
			}, "", "  ")
			return SuccessResult(string(data)), nil
		}

		// Set new state per FR-10.2.
		if err := svc.UpdateAgentState(
			ctx, p.AgentID, model.AgentState(p.State),
		); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Agent %s state → %s", p.AgentID, p.State)), nil
	})
}

func registerAgentWorkTool(reg *ToolRegistry, svc *service.AgentService) {
	reg.Register(ToolDef{
		Name:        "mtix_agent_work",
		Description: "Get the node an agent is currently working on",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"agent_id": {Type: "string", Description: "Agent ID"},
			},
			Required: []string{"agent_id"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct{ AgentID string `json:"agent_id"` }
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse agent_work args: %w", err)
		}

		node, err := svc.GetCurrentWork(ctx, p.AgentID)
		if err != nil {
			return nil, err
		}

		if node == nil {
			return SuccessResult(fmt.Sprintf("Agent %s has no current work", p.AgentID)), nil
		}

		data, _ := json.MarshalIndent(node, "", "  ")
		return SuccessResult(string(data)), nil
	})
}
