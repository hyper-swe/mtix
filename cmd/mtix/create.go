// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// newCreateCmd creates the mtix create command per FR-6.3.
func newCreateCmd() *cobra.Command {
	var (
		under       string
		nodeType    string
		priority    int
		description string
		prompt      string
		acceptance  string
		labels      string
		assign      string
	)

	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a new node",
		Long:  `Create a new node. Use --under to create a child node.`,
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runCreate(args[0], under, nodeType, priority,
				description, prompt, acceptance, labels, assign)
		}),
	}

	cmd.Flags().StringVar(&under, "under", "", "Parent node ID")
	cmd.Flags().StringVar(&nodeType, "type", "", "Node type (bug, feature, task, chore)")
	cmd.Flags().IntVar(&priority, "priority", 3, "Priority (1=critical, 5=backlog)")
	cmd.Flags().StringVar(&description, "description", "", "Node description")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Node prompt (FR-12.5)")
	cmd.Flags().StringVar(&acceptance, "acceptance", "", "Acceptance criteria")
	cmd.Flags().StringVar(&labels, "labels", "", "Comma-separated labels")
	cmd.Flags().StringVar(&assign, "assign", "", "Assign to agent/user")

	return cmd
}

// newMicroCmd creates the mtix micro shorthand command per FR-6.3.
func newMicroCmd() *cobra.Command {
	var (
		under      string
		prompt     string
		acceptance string
		labels     string
	)

	cmd := &cobra.Command{
		Use:   "micro <title>",
		Short: "Create a micro task (requires --under)",
		Long:  `Shorthand for 'create --under'. Requires a parent node.`,
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			if under == "" {
				return fmt.Errorf("--under is required for micro tasks")
			}
			return runCreate(args[0], under, "", 3,
				"", prompt, acceptance, labels, "")
		}),
	}

	cmd.Flags().StringVar(&under, "under", "", "Parent node ID (required)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Node prompt")
	cmd.Flags().StringVar(&acceptance, "acceptance", "", "Acceptance criteria")
	cmd.Flags().StringVar(&labels, "labels", "", "Comma-separated labels")
	_ = cmd.MarkFlagRequired("under")

	return cmd
}

func runCreate(title, under, nodeType string, priority int,
	description, prompt, acceptance, labels, assign string,
) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}

	// Determine project from config.
	project := "PROJ"
	if app.configSvc != nil {
		v, err := app.configSvc.Get("prefix")
		if err == nil {
			project = v
		}
	}

	req := &service.CreateNodeRequest{
		ParentID:    under,
		Project:     project,
		Title:       title,
		Description: description,
		Prompt:      prompt,
		Acceptance:  acceptance,
		Creator:     assign,
		Priority:    model.Priority(priority),
	}

	if labels != "" {
		req.Labels = strings.Split(labels, ",")
		for i := range req.Labels {
			req.Labels[i] = strings.TrimSpace(req.Labels[i])
		}
	}

	if req.Creator == "" {
		req.Creator = "cli"
	}

	_ = nodeType // IssueType handled in future refinement.

	ctx := context.Background()
	node, err := app.nodeSvc.CreateNode(ctx, req)
	if err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(node)
	}
	out.WriteHuman("○ Created %s: %s\n", node.ID, node.Title)

	// Warn if the task lacks context fields needed for agent pickup.
	if description == "" && prompt == "" && acceptance == "" {
		fmt.Fprintf(os.Stderr,
			"\n⚠ Warning: task %s has no description, prompt, or acceptance criteria.\n"+
				"  Agents cannot pick up tasks without context. Populate with:\n"+
				"    mtix update %s --description \"...\" --prompt \"...\" --acceptance \"...\"\n",
			node.ID, node.ID)
	}

	return nil
}
