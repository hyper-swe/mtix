// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// newShowCmd creates the mtix show command per FR-6.3.
func newShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show full details of a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runShow(args[0])
		},
	}
	return cmd
}

// newListCmd creates the mtix list command per FR-6.3 / FR-17.1.
// All filter flags accept comma-separated multiple values.
func newListCmd() *cobra.Command {
	var (
		status        string
		under         string
		assignee      string
		nodeType      string
		priority      string
		fields        string
		outputFormat  string
		maxFieldChars int
		showEmpty     bool
		limit         int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List nodes with filters",
		Aliases: []string{"ls"},
		RunE: func(_ *cobra.Command, _ []string) error {
			return runList(status, under, assignee, nodeType, priority, fields, outputFormat, maxFieldChars, showEmpty, limit)
		},
	}

	cmd.Flags().StringVar(&status, "status", "", "Filter by status (comma-separated for multiple)")
	cmd.Flags().StringVar(&under, "under", "", "Filter by parent subtree (comma-separated for multiple)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Filter by assignee (comma-separated for multiple)")
	cmd.Flags().StringVar(&nodeType, "type", "", "Filter by node type (comma-separated for multiple)")
	cmd.Flags().StringVar(&priority, "priority", "", "Filter by priority (comma-separated for multiple)")
	cmd.Flags().StringVar(&fields, "fields", "", "Restrict output to these fields (comma-separated)")
	cmd.Flags().StringVar(&outputFormat, "format", "", "Output format: briefing")
	cmd.Flags().IntVar(&maxFieldChars, "max-field-chars", 0, "Truncate field values (briefing format)")
	cmd.Flags().BoolVar(&showEmpty, "show-empty", false, "Include empty fields (briefing format)")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum results")

	return cmd
}

// newTreeCmd creates the mtix tree command per FR-6.3.
func newTreeCmd() *cobra.Command {
	var depth int

	cmd := &cobra.Command{
		Use:   "tree <id>",
		Short: "Show ASCII tree visualization",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runTree(args[0], depth)
		},
	}

	cmd.Flags().IntVar(&depth, "depth", 10, "Maximum tree depth")
	return cmd
}

// runShow displays full details of a single node with status icons and progress bars.
func runShow(id string) error {
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
		return out.WriteJSON(node)
	}

	icon := StatusIcon(string(node.Status))
	out.WriteHuman("ID:       %s\n", node.ID)
	out.WriteHuman("Title:    %s\n", node.Title)
	out.WriteHuman("Status:   %s %s\n", icon, node.Status)
	out.WriteHuman("Priority: %d\n", node.Priority)
	out.WriteHuman("Type:     %s\n", node.NodeType)
	if node.Assignee != "" {
		out.WriteHuman("Assignee: %s\n", node.Assignee)
	}
	if node.Description != "" {
		out.WriteHuman("Desc:     %s\n", node.Description)
	}
	if node.Prompt != "" {
		out.WriteHuman("Prompt:   %s\n", Truncate(node.Prompt, 100))
	}
	out.WriteHuman("Progress: %s\n", ProgressBar(node.Progress, 15))
	out.WriteHuman("Created:  %s\n", node.CreatedAt.Format("2006-01-02 15:04"))
	return nil
}

// runList displays nodes with status icons and aligned columns.
// Filter values are comma-separated strings parsed via splitCSV per FR-17.1.
// The fields parameter restricts output to the specified fields per FR-17.3.
// The outputFormat parameter selects "briefing" format per FR-17.4.
func runList(status, under, assignee, nodeType, priority, fields, outputFormat string, maxFieldChars int, showEmpty bool, limit int) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	priorities, err := splitCSVInts(priority)
	if err != nil {
		return fmt.Errorf("invalid --priority value: %w: %w", err, model.ErrInvalidInput)
	}

	ctx := context.Background()
	filter := store.NodeFilter{
		Under:    splitCSV(under),
		Assignee: splitCSV(assignee),
		NodeType: splitCSV(nodeType),
		Priority: priorities,
	}
	for _, s := range splitCSV(status) {
		filter.Status = append(filter.Status, model.Status(s))
	}

	fieldsList := splitCSV(fields)

	opts := store.ListOptions{Limit: limit}
	nodes, total, err := app.store.ListNodes(ctx, filter, opts)
	if err != nil {
		return err
	}

	// Apply natural sort per FR-17.6.
	format.SortNodes(nodes)

	// Briefing format per FR-17.4.
	if outputFormat == "briefing" {
		return format.RenderBriefing(os.Stdout, nodes, format.BriefingOpts{
			Fields:        fieldsList,
			MaxFieldChars: maxFieldChars,
			ShowEmpty:     showEmpty,
		})
	}

	out := NewOutputWriter(app.jsonOutput)

	if app.jsonOutput {
		if len(fieldsList) > 0 {
			projected, projErr := format.ProjectNodes(nodes, fieldsList)
			if projErr != nil {
				return projErr
			}
			return out.WriteJSON(map[string]any{
				"nodes": projected, "total": total,
			})
		}
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

// runTree displays an ASCII tree with status icons and connectors per FR-9.3.
func runTree(id string, maxDepth int) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	node, err := app.nodeSvc.GetNode(ctx, id)
	if err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)

	if app.jsonOutput {
		return out.WriteJSON(node)
	}

	printTreeFormatted(ctx, out, id, "", true, maxDepth, 0,
		node.Title, string(node.Status), node.Progress)
	return nil
}

// printTreeFormatted recursively prints an ASCII tree with status icons.
func printTreeFormatted(ctx context.Context, out OutputWriter, id, prefix string, isLast bool, maxDepth, depth int, title, status string, progress float64) {
	line := TreeLine(id, status, title, progress, prefix, isLast, depth, false)
	out.WriteHuman("%s\n", line)

	if depth >= maxDepth {
		return
	}

	children, err := app.store.GetDirectChildren(ctx, id)
	if err != nil {
		return
	}

	childPrefix := TreeChildPrefix(prefix, isLast, depth)

	for i, child := range children {
		printTreeFormatted(ctx, out, child.ID, childPrefix, i == len(children)-1,
			maxDepth, depth+1, child.Title, string(child.Status), child.Progress)
	}
}
