// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
)

// newClaimCmd creates the mtix claim command per FR-10.4.
func newClaimCmd() *cobra.Command {
	var agentID string

	cmd := &cobra.Command{
		Use:   "claim <id>",
		Short: "Claim a node for an agent",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runClaim(args[0], agentID)
		}),
	}

	cmd.Flags().StringVar(&agentID, "agent", "", "Agent ID (required)")
	_ = cmd.MarkFlagRequired("agent")

	return cmd
}

func runClaim(id, agentID string) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.store.ClaimNode(ctx, id, agentID); err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(map[string]string{
			"id": id, "agent": agentID, "status": "claimed",
		})
	}
	out.WriteHuman("● Claimed %s for agent %s\n", id, agentID)
	return nil
}

// newUnclaimCmd creates the mtix unclaim command per FR-10.4.
func newUnclaimCmd() *cobra.Command {
	var reason string

	cmd := &cobra.Command{
		Use:   "unclaim <id>",
		Short: "Release a node assignment",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runUnclaim(args[0], reason)
		}),
	}

	cmd.Flags().StringVar(&reason, "reason", "", "Reason for unclaiming (required)")
	_ = cmd.MarkFlagRequired("reason")

	return cmd
}

func runUnclaim(id, reason string) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.store.UnclaimNode(ctx, id, reason, "cli"); err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(map[string]string{"id": id, "status": "unclaimed"})
	}
	out.WriteHuman("○ Unclaimed %s\n", id)
	return nil
}

// newDoneCmd creates the mtix done command per FR-6.3.
func newDoneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "done <id>",
		Short: "Mark a node as done",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runTransition(args[0], model.StatusDone, "marked done via CLI")
		}),
	}
}

// newDeferCmd creates the mtix defer command per FR-3.8.
func newDeferCmd() *cobra.Command {
	var until string

	cmd := &cobra.Command{
		Use:   "defer <id>",
		Short: "Defer a node until a specified time",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runDefer(args[0], until)
		}),
	}

	cmd.Flags().StringVar(&until, "until", "", "Defer until (ISO-8601 timestamp)")

	return cmd
}

func runDefer(id, until string) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	// Validate the until timestamp if provided.
	if until != "" {
		if _, err := time.Parse(time.RFC3339, until); err != nil {
			return fmt.Errorf("invalid --until timestamp: %w", err)
		}
	}

	ctx := context.Background()
	if err := app.nodeSvc.TransitionStatus(
		ctx, id, model.StatusDeferred, "deferred via CLI", "cli",
	); err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(map[string]string{"id": id, "status": "deferred"})
	}
	out.WriteHuman("⏸ Deferred %s\n", id)
	return nil
}

// newCancelCmd creates the mtix cancel command per FR-6.3.
func newCancelCmd() *cobra.Command {
	var (
		reason  string
		cascade bool
	)

	cmd := &cobra.Command{
		Use:   "cancel <id>",
		Short: "Cancel a node with mandatory reason",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runCancel(args[0], reason, cascade)
		}),
	}

	cmd.Flags().StringVar(&reason, "reason", "", "Cancellation reason (required)")
	cmd.Flags().BoolVar(&cascade, "cascade", false, "Cancel all descendants too")
	_ = cmd.MarkFlagRequired("reason")

	return cmd
}

func runCancel(id, reason string, cascade bool) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.store.CancelNode(ctx, id, reason, "cli", cascade); err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(map[string]string{
			"id": id, "status": "cancelled", "reason": reason,
		})
	}
	out.WriteHuman("✕ Cancelled %s: %s\n", id, reason)
	return nil
}

// newReopenCmd creates the mtix reopen command per FR-6.3.
func newReopenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reopen <id>",
		Short: "Reopen a closed node",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runTransition(args[0], model.StatusOpen, "reopened via CLI")
		}),
	}
}

// runTransition is a helper for simple status transitions with status icons.
func runTransition(id string, status model.Status, reason string) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.nodeSvc.TransitionStatus(ctx, id, status, reason, "cli"); err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(map[string]string{
			"id": id, "status": string(status),
		})
	}
	icon := StatusIcon(string(status))
	out.WriteHuman("%s %s → %s\n", icon, id, status)
	return nil
}
