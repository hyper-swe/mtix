// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
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
	)

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a node's fields",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runUpdate(args[0], title, description, prompt,
				acceptance, priority, labels, assignee)
		}),
	}

	cmd.Flags().StringVar(&title, "title", "", "New title")
	cmd.Flags().StringVar(&description, "description", "", "New description")
	cmd.Flags().StringVar(&prompt, "prompt", "", "New prompt")
	cmd.Flags().StringVar(&acceptance, "acceptance", "", "New acceptance criteria")
	cmd.Flags().IntVar(&priority, "priority", 0, "New priority (1-5)")
	cmd.Flags().StringVar(&labels, "labels", "", "New labels (comma-separated)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "New assignee")

	return cmd
}

func runUpdate(id, title, description, prompt, acceptance string,
	priority int, labels, assignee string,
) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	updates := &store.NodeUpdate{}
	hasUpdate := false

	if title != "" {
		updates.Title = &title
		hasUpdate = true
	}
	if description != "" {
		updates.Description = &description
		hasUpdate = true
	}
	if prompt != "" {
		updates.Prompt = &prompt
		hasUpdate = true
	}
	if acceptance != "" {
		updates.Acceptance = &acceptance
		hasUpdate = true
	}
	if priority > 0 {
		p := model.Priority(priority)
		updates.Priority = &p
		hasUpdate = true
	}
	if labels != "" {
		updates.Labels = splitAndTrim(labels)
		hasUpdate = true
	}
	if assignee != "" {
		updates.Assignee = &assignee
		hasUpdate = true
	}

	if !hasUpdate {
		return fmt.Errorf("no fields to update (use flags like --title, --priority)")
	}

	ctx := context.Background()
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
