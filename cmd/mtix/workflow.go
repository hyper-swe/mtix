// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
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

	ctx := mutationContext()
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

	ctx := mutationContext()
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

// deferLongHelp describes what `mtix defer` stores and how the wake time
// behaves (FR-3.8b, MTIX-95.22). Keep docs/CLI_REFERENCE.md in step with it.
const deferLongHelp = `Move a node to deferred, optionally with a wake time.

With --until, the wake time is stored with the deferral. It must be an
RFC 3339 timestamp with a zone, such as 2026-04-01T00:00:00Z or
2026-04-01T09:00:00+05:30, whose UTC year is 1 to 9999, and is stored in
UTC in whole seconds. Before the wake time, a claim is refused. From the
wake time on, mtix ready lists the node and a claim succeeds, and the next
background pass (mtix gc, or POST /api/v1/admin/gc on a running server)
reopens it.

Without --until the node has no wake time, and any earlier wake time is
cleared; mtix ready lists it and it can be claimed at once. Deferring a
node that is already deferred replaces its wake time. Leaving deferred
(the background pass, reopen, claim or cancel) clears the wake time.

In 0.5.x hub sync does not carry the wake time: other machines receive the
deferral as a status change without it. A git-tracked .mtix/tasks.json
does carry it (defer_until), so a machine that imports that file gets it.

The stored wake time is the defer_until field of mtix show <id> --json.`

// newDeferCmd creates the mtix defer command per FR-3.8 and FR-3.8b.
func newDeferCmd() *cobra.Command {
	var until string

	cmd := &cobra.Command{
		Use:   "defer <id>",
		Short: "Defer a node until a specified time",
		Long:  deferLongHelp,
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runDefer(args[0], until)
		}),
	}

	cmd.Flags().StringVar(&until, "until", "",
		"Wake time, RFC 3339 with a zone (e.g. 2026-04-01T00:00:00Z); omit for no wake time")

	return cmd
}

// runDefer defers a node, storing the --until wake time in the same
// transaction as the transition (FR-3.8b, MTIX-95.22). The timestamp is
// parsed here at the boundary; the service stores it in UTC. The author is
// the process identity (MTIX-24), never a hard-coded "cli".
func runDefer(id, until string) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	wake, err := service.ParseDeferUntil(until)
	if err != nil {
		return fmt.Errorf("invalid --until timestamp: %w", err)
	}

	ctx := mutationContext()
	if err := app.nodeSvc.DeferNode(ctx, id, wake, "deferred via CLI", app.authorID); err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		result := map[string]string{"id": id, "status": "deferred"}
		if wake != nil {
			result["defer_until"] = wake.Format(time.RFC3339)
		}
		return out.WriteJSON(result)
	}
	if wake != nil {
		out.WriteHuman("⏸ Deferred %s until %s\n", id, wake.Format(time.RFC3339))
		return nil
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

	ctx := mutationContext()
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

	ctx := mutationContext()
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
