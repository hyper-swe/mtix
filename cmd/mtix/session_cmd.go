// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// newSessionCmd creates the mtix session command group per FR-10.5a.
func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage agent sessions",
	}

	cmd.AddCommand(
		newSessionStartCmd(),
		newSessionEndCmd(),
		newSessionSummaryCmd(),
	)
	return cmd
}

// newSessionStartCmd creates the mtix session start command.
func newSessionStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <agent-id>",
		Short: "Start a new session for an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runSessionStart(args[0])
		},
	}
}

func runSessionStart(agentID string) error {
	if app.sessionSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	project := "PROJ"
	if app.configSvc != nil {
		if v, err := app.configSvc.Get("prefix"); err == nil {
			project = v
		}
	}

	ctx := context.Background()
	sessionID, err := app.sessionSvc.SessionStart(ctx, agentID, project)
	if err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"session_id": sessionID, "agent_id": agentID, "status": "active",
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Started session %s for %s\n", sessionID, agentID)
	}
	return nil
}

// newSessionEndCmd creates the mtix session end command.
func newSessionEndCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "end <agent-id>",
		Short: "End the active session for an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runSessionEnd(args[0])
		},
	}
}

func runSessionEnd(agentID string) error {
	if app.sessionSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.sessionSvc.SessionEnd(ctx, agentID); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"agent_id": agentID, "status": "ended",
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Ended session for %s\n", agentID)
	}
	return nil
}

// newSessionSummaryCmd creates the mtix session summary command.
func newSessionSummaryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "summary <agent-id>",
		Short: "Show summary of most recent session",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runSessionSummary(args[0])
		},
	}
}

func runSessionSummary(agentID string) error {
	if app.sessionSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	summary, err := app.sessionSvc.SessionSummary(ctx, agentID)
	if err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("Session: %s (%s)\n", summary.SessionID, summary.Status)
		fmt.Printf("Started: %s\n", summary.StartedAt.Format("2006-01-02 15:04"))
		if summary.EndedAt != nil {
			fmt.Printf("Ended:   %s\n", summary.EndedAt.Format("2006-01-02 15:04"))
		}
		fmt.Printf("Created: %d  Completed: %d  Deferred: %d\n",
			summary.NodesCreated, summary.NodesCompleted, summary.NodesDeferred)
		if summary.SummaryText != "" {
			fmt.Printf("Summary: %s\n", summary.SummaryText)
		}
	}
	return nil
}
