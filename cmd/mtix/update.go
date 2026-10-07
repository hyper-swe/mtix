// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// newUpdateCmd creates the mtix update command per FR-6.3.
func newUpdateCmd() *cobra.Command {
	var (
		title       string
		description string
		prompt      string
		acceptance  string
		priority    int
		labels      string
		assignee    string
		issueType   string
	)

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a node's fields",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(cmd *cobra.Command, args []string) error {
			updates := buildCLIUpdate(cliUpdateInput{title, description, prompt, acceptance, priority, labels, assignee})
			if cmd.Flags().Changed("assignee") {
				updates.Assignee = &assignee
			}
			if cmd.Flags().Changed("type") {
				kind := model.IssueType(issueType)
				updates.IssueType = &kind
			}
			return applyCLIUpdate(args[0], updates)
		}),
	}

	cmd.Flags().StringVar(&issueType, "type", "", "New work classification (bug, feature, task, chore, refactor, test, doc); empty clears, omission preserves; list --type filters hierarchy")
	cmd.Flags().StringVar(&title, "title", "", "New title")
	cmd.Flags().StringVar(&description, "description", "", "New description")
	cmd.Flags().StringVar(&prompt, "prompt", "", "New prompt")
	cmd.Flags().StringVar(&acceptance, "acceptance", "", "New acceptance criteria")
	cmd.Flags().IntVar(&priority, "priority", 0, "New priority (1-5)")
	cmd.Flags().StringVar(&labels, "labels", "", "New labels (comma-separated)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "New assignee; empty clears, omission preserves; nonempty raw IDs: max 64 UTF-8 bytes, no whitespace-only, control or invisible format characters")

	return cmd
}

// cliUpdateInput carries the legacy CLI update values without changing wire APIs.
type cliUpdateInput struct {
	title, description, prompt, acceptance string
	priority                               int
	labels, assignee                       string
}

func runUpdate(id, title, description, prompt, acceptance string,
	priority int, labels, assignee string, issueType ...model.IssueType,
) error {
	updates := buildCLIUpdate(cliUpdateInput{title, description, prompt, acceptance, priority, labels, assignee})
	if len(issueType) > 0 {
		updates.IssueType = &issueType[0]
	}
	return applyCLIUpdate(id, updates)
}

// buildCLIUpdate preserves legacy omission behavior; Cobra supplies explicit empties.
func buildCLIUpdate(in cliUpdateInput) *store.NodeUpdate {
	updates := &store.NodeUpdate{}
	if in.title != "" {
		updates.Title = &in.title
	}
	if in.description != "" {
		updates.Description = &in.description
	}
	if in.prompt != "" {
		updates.Prompt = &in.prompt
	}
	if in.acceptance != "" {
		updates.Acceptance = &in.acceptance
	}
	if in.priority > 0 {
		p := model.Priority(in.priority)
		updates.Priority = &p
	}
	if in.labels != "" {
		updates.Labels = splitAndTrim(in.labels)
	}
	if in.assignee != "" {
		updates.Assignee = &in.assignee
	}
	return updates
}

func applyCLIUpdate(id string, updates *store.NodeUpdate) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}
	if updates.IssueType == nil && updates.Title == nil && updates.Description == nil && updates.Prompt == nil && updates.Acceptance == nil && updates.Priority == nil && updates.Labels == nil && updates.Assignee == nil {
		return fmt.Errorf("no fields to update (use flags like --title, --priority)")
	}
	ctx := mutationContext()
	if err := app.nodeSvc.UpdateNode(ctx, id, updates); err != nil {
		return err
	}
	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{"id": id, "status": "updated"})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Updated %s\n", id)
	}
	return nil
}

// splitAndTrim splits a comma-separated string and trims whitespace.
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
