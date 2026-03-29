// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// newStatsCmd creates the mtix stats command per FR-6.3.
func newStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show project statistics",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runStats()
		},
	}
}

// runStats displays project statistics with status icons per FR-6.2.
func runStats() error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	statuses := []model.Status{
		model.StatusOpen, model.StatusInProgress, model.StatusDone,
		model.StatusBlocked, model.StatusDeferred, model.StatusCancelled,
		model.StatusInvalidated,
	}

	counts := make(map[string]int)
	total := 0
	for _, s := range statuses {
		filter := store.NodeFilter{Status: []model.Status{s}}
		_, count, err := app.store.ListNodes(ctx, filter, store.ListOptions{Limit: 0})
		if err != nil {
			return err
		}
		counts[string(s)] = count
		total += count
	}
	counts["total"] = total

	out := NewOutputWriter(app.jsonOutput)

	if app.jsonOutput {
		return out.WriteJSON(counts)
	}

	out.WriteHuman("Project Statistics:\n")
	out.WriteHuman("  Total:              %d\n", total)
	for _, s := range statuses {
		icon := StatusIcon(string(s))
		out.WriteHuman("  %s %-14s %d\n", icon, s, counts[string(s)])
	}

	// Overall progress bar.
	done := counts["done"]
	if total > 0 {
		progress := float64(done) / float64(total)
		out.WriteHuman("\n  Overall: %s\n", ProgressBar(progress, 20))
	}
	return nil
}

// newProgressCmd creates the mtix progress command per FR-5.1.
func newProgressCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "progress <id>",
		Short: "Show progress for a node and its subtree",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runProgress(args[0])
		},
	}
}

// runProgress displays node progress with progress bars per FR-5.1.
func runProgress(id string) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	node, err := app.nodeSvc.GetNode(ctx, id)
	if err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)

	if app.jsonOutput {
		return out.WriteJSON(map[string]any{
			"id": id, "progress": node.Progress, "status": node.Status,
		})
	}

	icon := StatusIcon(string(node.Status))
	out.WriteHuman("%s %s %s: %s\n", icon, id, node.Status, ProgressBar(node.Progress, 20))

	// Show children progress if any.
	children, err := app.store.GetDirectChildren(ctx, id)
	if err == nil && len(children) > 0 {
		out.WriteHuman("Children:\n")
		for _, c := range children {
			cIcon := StatusIcon(string(c.Status))
			out.WriteHuman("  %s %-12s %s %s\n",
				cIcon, c.ID, ProgressBar(c.Progress, 10), Truncate(c.Title, 40))
		}
	}
	return nil
}
