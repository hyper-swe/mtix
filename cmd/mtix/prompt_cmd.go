// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
)

// newPromptCmd creates the mtix prompt command per FR-12.5.
func newPromptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prompt <id> <text>",
		Short: "Set or update a node's prompt text",
		Args:  cobra.ExactArgs(2),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runPrompt(args[0], args[1])
		}),
	}
}

func runPrompt(id, text string) error {
	if app.promptSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.promptSvc.UpdatePrompt(ctx, id, text, "cli"); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{"id": id, "status": "prompt_updated"})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Updated prompt for %s\n", id)
	}
	return nil
}

// newAnnotateCmd creates the mtix annotate command per FR-3.4.
func newAnnotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "annotate <id> <text>",
		Short: "Add an annotation to a node",
		Args:  cobra.ExactArgs(2),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runAnnotate(args[0], args[1])
		}),
	}
}

func runAnnotate(id, text string) error {
	if app.promptSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.promptSvc.AddAnnotation(ctx, id, text, "cli"); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{"id": id, "status": "annotated"})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Added annotation to %s\n", id)
	}
	return nil
}

// newResolveAnnotationCmd creates the mtix resolve-annotation command per FR-3.4.
func newResolveAnnotationCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resolve-annotation <node-id> <annotation-id>",
		Short: "Resolve an annotation on a node",
		Args:  cobra.ExactArgs(2),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runResolveAnnotation(args[0], args[1])
		}),
	}
}

func runResolveAnnotation(nodeID, annotID string) error {
	if app.promptSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.promptSvc.ResolveAnnotation(ctx, nodeID, annotID, true); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"node_id": nodeID, "annotation_id": annotID, "status": "resolved",
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Resolved annotation %s on %s\n", annotID, nodeID)
	}
	return nil
}

// newContextCmd creates the mtix context command per FR-12.2.
func newContextCmd() *cobra.Command {
	var maxTokens int

	cmd := &cobra.Command{
		Use:   "context <id>",
		Short: "Show assembled context chain for a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runContext(args[0], maxTokens)
		},
	}

	cmd.Flags().IntVar(&maxTokens, "max-tokens", 0,
		"Maximum token budget for assembled prompt (0 = unlimited)")

	return cmd
}

func runContext(id string, maxTokens int) error {
	if app.ctxSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	opts := &service.ContextOptions{MaxTokens: maxTokens}
	ctx := context.Background()
	resp, err := app.ctxSvc.GetContext(ctx, id, opts)
	if err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Println("=== Context Chain ===")
		for _, entry := range resp.Chain {
			indent := ""
			for i := 0; i < entry.Depth; i++ {
				indent += "  "
			}
			fmt.Printf("%s%s [%s] %s\n", indent, entry.ID, entry.Status, entry.Title)
		}
		if resp.AssembledPrompt != "" {
			fmt.Printf("\n=== Assembled Prompt (%d chars) ===\n%s\n",
				len(resp.AssembledPrompt), resp.AssembledPrompt)
		}
	}
	return nil
}
