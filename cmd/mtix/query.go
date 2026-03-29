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

// newSearchCmd creates the mtix search command per FR-6.3.
func newSearchCmd() *cobra.Command {
	var (
		status   string
		assignee string
		nodeType string
		limit    int
	)

	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search nodes with advanced filters",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runSearch(status, assignee, nodeType, limit)
		},
	}

	cmd.Flags().StringVar(&status, "status", "", "Filter by status")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Filter by assignee")
	cmd.Flags().StringVar(&nodeType, "type", "", "Filter by node type")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum results")

	return cmd
}

func runSearch(status, assignee, nodeType string, limit int) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	filter := store.NodeFilter{
		Assignee: assignee,
		NodeType: nodeType,
	}
	if status != "" {
		filter.Status = []model.Status{model.Status(status)}
	}

	ctx := context.Background()
	nodes, total, err := app.store.ListNodes(ctx, filter, store.ListOptions{Limit: limit})
	if err != nil {
		return err
	}

	return printNodeList(nodes, total)
}

// newReadyCmd creates the mtix ready command per FR-6.3.
func newReadyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ready",
		Short: "List nodes ready for work (unblocked, unassigned)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runReady()
		},
	}
}

func runReady() error {
	if app.bgSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	nodes, err := app.bgSvc.GetReadyNodes(ctx)
	if err != nil {
		return err
	}

	return printNodeList(nodes, len(nodes))
}

// newBlockedCmd creates the mtix blocked command per FR-6.3.
func newBlockedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "blocked",
		Short: "List nodes blocked by dependencies",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runBlocked()
		},
	}
}

func runBlocked() error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	filter := store.NodeFilter{
		Status: []model.Status{model.StatusBlocked},
	}

	ctx := context.Background()
	nodes, total, err := app.store.ListNodes(ctx, filter, store.ListOptions{Limit: 50})
	if err != nil {
		return err
	}

	return printNodeList(nodes, total)
}

// newStaleCmd creates the mtix stale command per FR-6.3.
func newStaleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stale",
		Short: "List nodes with stale agent assignments",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runStale()
		},
	}
}

func runStale() error {
	if app.agentSvc == nil || app.configSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	threshold := app.configSvc.AgentStaleThreshold()
	ctx := context.Background()
	agents, err := app.agentSvc.GetStaleAgents(ctx, threshold)
	if err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)

	if app.jsonOutput {
		return out.WriteJSON(agents)
	}

	if len(agents) == 0 {
		out.WriteHuman("No stale agents found\n")
	} else {
		out.WriteHuman("Stale agents:\n")
		for _, a := range agents {
			out.WriteHuman("  %s\n", a)
		}
	}
	return nil
}

// newOrphansCmd creates the mtix orphans command per FR-6.3.
func newOrphansCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "orphans",
		Short: "List root-level nodes (no parent)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runOrphans()
		},
	}
}

func runOrphans() error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	// Root nodes have no parent — list with empty under filter.
	filter := store.NodeFilter{}
	ctx := context.Background()
	nodes, total, err := app.store.ListNodes(ctx, filter, store.ListOptions{Limit: 100})
	if err != nil {
		return err
	}

	// Filter to only root nodes (empty parent_id).
	var roots []*model.Node
	for _, n := range nodes {
		if n.ParentID == "" {
			roots = append(roots, n)
		}
	}

	return printNodeList(roots, total)
}

// printNodeList formats and prints a list of nodes using OutputWriter with status icons.
func printNodeList(nodes []*model.Node, total int) error {
	out := NewOutputWriter(app.jsonOutput)

	if app.jsonOutput {
		return out.WriteJSON(map[string]any{
			"nodes": nodes, "total": total,
		})
	}

	headers := []string{"ID", "Status", "Pri", "Progress", "Title"}
	rows := make([][]string, 0, len(nodes))
	for _, n := range nodes {
		icon := StatusIcon(string(n.Status))
		rows = append(rows, []string{
			n.ID,
			fmt.Sprintf("%s %s", icon, n.Status),
			fmt.Sprintf("%d", n.Priority),
			fmt.Sprintf("%.0f%%", n.Progress*100),
			Truncate(n.Title, 50),
		})
	}
	out.WriteTable(headers, rows)

	if total > len(nodes) {
		out.WriteHuman("\n(%d of %d shown)\n", len(nodes), total)
	}
	return nil
}
