// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// newCommentCmd creates the mtix comment command per FR-6.3 / FR-19.1.
func newCommentCmd() *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "comment <id> <text>",
		Short: "Add a comment annotation to a node",
		Long: `Add a comment annotation to a node.

Use --to <agent> to address the comment at a specific agent; it then lands in
that agent's inbox (see 'mtix inbox'), which is how a worker gets woken by a
ruling without a human relay (FR-19.1).`,
		Args: cobra.ExactArgs(2),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runComment(args[0], args[1], to)
		}),
	}
	cmd.Flags().StringVar(&to, "to", "", "Address the comment to an agent (delivers to its inbox)")
	return cmd
}

func runComment(id, text, to string) error {
	if app.promptSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.promptSvc.AddAnnotation(ctx, id, text, "cli", to); err != nil {
		return err
	}

	switch {
	case app.jsonOutput:
		out := map[string]string{"id": id, "status": "annotation_added"}
		if to != "" {
			out["to"] = to
		}
		data, _ := json.Marshal(out)
		fmt.Println(string(data))
	case to != "":
		fmt.Printf("Added annotation to %s (→ %s inbox)\n", id, to)
	default:
		fmt.Printf("Added annotation to %s\n", id)
	}
	return nil
}
