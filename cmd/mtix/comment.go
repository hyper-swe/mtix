// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// newCommentCmd creates the mtix comment command per FR-6.3.
func newCommentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "comment <id> <text>",
		Short: "Add a comment annotation to a node",
		Args:  cobra.ExactArgs(2),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runComment(args[0], args[1])
		}),
	}
}

func runComment(id, text string) error {
	if app.promptSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.promptSvc.AddAnnotation(ctx, id, text, "cli"); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"id": id, "status": "annotation_added",
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Added annotation to %s\n", id)
	}
	return nil
}
