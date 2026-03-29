// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
)

// newDecomposeCmd creates the mtix decompose command per FR-6.3.
// Supports two modes:
//   - Positional args: mtix decompose PARENT "Title A" "Title B"
//   - JSONL file/stdin: mtix decompose PARENT --file plan.jsonl
//     or: mtix decompose PARENT --file - (reads from stdin)
//
// JSONL mode is preferred for LLM plan output — each line is an
// independent JSON object with title, prompt, acceptance, description,
// priority, and labels. Partial output is still usable.
func newDecomposeCmd() *cobra.Command {
	var planFile string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "decompose <parent-id> [title1 title2...]",
		Short: "Create multiple children under a node atomically",
		Long: `Create multiple child nodes under a parent in a single atomic operation.
All children are created together or none at all.

Two modes:
  Positional args:  mtix decompose PROJ-1 "Task A" "Task B"
  JSONL file:       mtix decompose PROJ-1 --file plan.jsonl
  JSONL stdin:      mtix decompose PROJ-1 --file -

Use --dry-run to preview proposed nodes without creating them:
  mtix decompose PROJ-1 --file plan.jsonl --dry-run

JSONL format (one JSON object per line):
  {"title":"Add login endpoint","prompt":"Implement POST /auth/login..."}
  {"title":"Add token refresh","prompt":"...","priority":1,"labels":["auth"]}

JSONL is preferred for LLM plan output:
  - Each line is independently valid (partial output is usable)
  - No bracket matching or trailing comma issues
  - Streaming-friendly and append-friendly`,
		Args: cobra.MinimumNArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			parentID := args[0]
			titles := args[1:]

			// Build children from either --file or positional args.
			var children []service.DecomposeInput
			if planFile != "" {
				inputs, err := parseJSONLFile(planFile)
				if err != nil {
					return err
				}
				children = service.ToDecomposeInputs(inputs)
			} else {
				if len(titles) == 0 {
					return fmt.Errorf("provide child titles as arguments or use --file")
				}
				children = make([]service.DecomposeInput, len(titles))
				for i, t := range titles {
					children[i] = service.DecomposeInput{Title: t}
				}
			}

			if dryRun {
				return runDecomposeDryRun(parentID, children)
			}
			return runDecomposeChildren(parentID, children)
		}),
	}

	cmd.Flags().StringVarP(&planFile, "file", "f", "",
		"JSONL plan file (use - for stdin)")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false,
		"Preview proposed nodes without creating them")

	return cmd
}

// runDecompose creates children from positional title arguments.
func runDecompose(parentID string, titles []string) error {
	children := make([]service.DecomposeInput, len(titles))
	for i, t := range titles {
		children[i] = service.DecomposeInput{Title: t}
	}
	return runDecomposeChildren(parentID, children)
}

// runDecomposeChildren creates children under the parent node.
func runDecomposeChildren(parentID string, children []service.DecomposeInput) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	ids, err := app.nodeSvc.Decompose(ctx, parentID, children, "cli")
	if err != nil {
		return err
	}

	return printDecomposeResult(parentID, ids, children)
}

// runDecomposeDryRun previews proposed nodes without creating them.
func runDecomposeDryRun(parentID string, children []service.DecomposeInput) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	proposed, err := app.nodeSvc.DecomposeDryRun(ctx, parentID, children)
	if err != nil {
		return err
	}

	return printDryRunResult(parentID, proposed)
}

// printDryRunResult outputs the dry-run preview in JSON or text format.
func printDryRunResult(parentID string, proposed []service.ProposedNode) error {
	if app.jsonOutput {
		data, _ := json.Marshal(map[string]any{
			"dry_run": true, "parent": parentID, "proposed": proposed,
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Dry-run: %d proposed children under %s\n", len(proposed), parentID)
		for _, p := range proposed {
			desc := p.Description
			if len(desc) > 80 {
				desc = desc[:77] + "..."
			}
			if desc != "" {
				fmt.Printf("  %s  %s  (%s)\n", p.ID, p.Title, desc)
			} else {
				fmt.Printf("  %s  %s\n", p.ID, p.Title)
			}
		}
	}
	return nil
}

// parseJSONLFile reads and parses a JSONL plan file.
// If filePath is "-", reads from stdin.
func parseJSONLFile(filePath string) ([]service.JSONLInput, error) {
	if filePath == "-" {
		return service.ParseJSONL(os.Stdin)
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open plan file %s: %w", filePath, err)
	}
	defer f.Close()

	return service.ParseJSONL(f)
}

// printDecomposeResult outputs the decompose result in JSON or text format.
// Emits a warning when children lack context fields (description, prompt,
// acceptance) that agents need for self-sufficient task pickup.
func printDecomposeResult(parentID string, ids []string, children []service.DecomposeInput) error {
	if app.jsonOutput {
		data, _ := json.Marshal(map[string]any{
			"parent": parentID, "created": ids,
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Created %d children under %s:\n", len(ids), parentID)
		for _, id := range ids {
			fmt.Printf("  %s\n", id)
		}
	}

	// Warn about incomplete tasks so agents populate context before pickup.
	incomplete := countIncompleteChildren(children)
	if incomplete > 0 {
		fmt.Fprintf(os.Stderr,
			"\n⚠ Warning: %d of %d children have no description, prompt, or acceptance criteria.\n"+
				"  Agents cannot pick up tasks without context. Populate them with:\n"+
				"    mtix update <id> --description \"...\" --prompt \"...\" --acceptance \"...\"\n",
			incomplete, len(children))
	}

	return nil
}

// countIncompleteChildren returns how many children lack context fields.
func countIncompleteChildren(children []service.DecomposeInput) int {
	count := 0
	for _, c := range children {
		if c.Description == "" && c.Prompt == "" && c.Acceptance == "" {
			count++
		}
	}
	return count
}
