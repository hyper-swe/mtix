// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
)

// newAgentCmd creates the mtix agent command group per FR-10.2.
func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage agent lifecycle",
	}

	cmd.AddCommand(
		newAgentRegisterCmd(),
		newAgentStateCmd(),
		newAgentHeartbeatCmd(),
		newAgentWorkCmd(),
	)
	return cmd
}

// newAgentRegisterCmd creates the mtix agent register command per FR-10.1a.
func newAgentRegisterCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "register <agent-id>",
		Short: "Register a new agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runAgentRegister(args[0])
		},
	}
}

func runAgentRegister(agentID string) error {
	if app.agentSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	project := ""
	if app.configSvc != nil {
		if v, err := app.configSvc.Get("prefix"); err == nil {
			project = v
		}
	}

	ctx := context.Background()
	if err := app.agentSvc.RegisterAgent(ctx, agentID, project); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"agent_id": agentID, "status": "registered",
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Registered agent %s\n", agentID)
	}
	return nil
}

// newAgentStateCmd creates the mtix agent state command.
func newAgentStateCmd() *cobra.Command {
	var state string

	cmd := &cobra.Command{
		Use:   "state <agent-id>",
		Short: "Get or set agent state",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if state != "" {
				return runSetAgentState(args[0], state)
			}
			return runGetAgentState(args[0])
		},
	}

	cmd.Flags().StringVar(&state, "set", "",
		"Set state (idle, working, stuck, done)")

	return cmd
}

func runGetAgentState(agentID string) error {
	if app.agentSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	state, err := app.agentSvc.GetAgentState(ctx, agentID)
	if err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"agent_id": agentID, "state": string(state),
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("%s: %s\n", agentID, state)
	}
	return nil
}

func runSetAgentState(agentID, state string) error {
	if app.agentSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.agentSvc.UpdateAgentState(
		ctx, agentID, model.AgentState(state),
	); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"agent_id": agentID, "state": state, "status": "updated",
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("%s → %s\n", agentID, state)
	}
	return nil
}

// newAgentHeartbeatCmd creates the mtix agent heartbeat command.
func newAgentHeartbeatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "heartbeat <agent-id>",
		Short: "Send a heartbeat for an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runAgentHeartbeat(args[0])
		},
	}
}

func runAgentHeartbeat(agentID string) error {
	if app.agentSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.agentSvc.Heartbeat(ctx, agentID); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"agent_id": agentID, "status": "heartbeat_sent",
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Heartbeat sent for %s\n", agentID)
	}
	return nil
}

// newAgentWorkCmd creates the mtix agent work command.
func newAgentWorkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "work <agent-id>",
		Short: "Show current work assignment for an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runAgentWork(args[0])
		},
	}
}

func runAgentWork(agentID string) error {
	if app.agentSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	node, err := app.agentSvc.GetCurrentWork(ctx, agentID)
	if err != nil {
		return err
	}

	if node == nil {
		if app.jsonOutput {
			fmt.Println("{\"node\": null}")
		} else {
			fmt.Printf("No current work for %s\n", agentID)
		}
		return nil
	}

	if app.jsonOutput {
		data, _ := json.MarshalIndent(node, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("%s is working on: %s — %s\n", agentID, node.ID, node.Title)
	}
	return nil
}
