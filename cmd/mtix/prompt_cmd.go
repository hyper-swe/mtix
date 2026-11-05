// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/format"
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

	ctx := mutationContext()
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
	var to string
	cmd := &cobra.Command{
		Use:   "annotate <id> <text>",
		Short: "Add an annotation to a node",
		Long: `Add an annotation to a node.

Use --to <agent> to address the annotation at a specific agent: it then takes
exactly the path of 'mtix comment --to' and lands in that agent's inbox (see
'mtix inbox'), the same as the MCP mtix_annotate tool with 'to'. Without --to
the annotation is stored on the node as before. For a reply window use
'mtix comment --to <agent> --await-reply'.`,
		Args: cobra.ExactArgs(2),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runAnnotate(args[0], args[1], to)
		}),
	}
	cmd.Flags().StringVar(&to, "to", "", "Address the annotation to an agent (delivers to its inbox, like comment --to)")
	return cmd
}

// runAnnotate stores an annotation on a node (FR-3.4). A non-empty to sends it
// through the comment path, so annotate --to and comment --to share one
// addressed-send implementation (MTIX-112.31); an empty to keeps the plain
// annotation path.
func runAnnotate(id, text, to string) error {
	if to != "" {
		return runComment(id, text, to)
	}
	if app.promptSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := mutationContext()
	// MTIX-24 process identity, not hardcoded "cli" (see runComment).
	if err := app.promptSvc.AddAnnotation(ctx, id, text, app.authorID, ""); err != nil {
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
		Long: `Resolve an annotation on a node.

The annotation id is the full id (as in mtix show --json) or the short id that
mtix show prints, which is the first 12 characters. A prefix works when it is at
least 12 characters and matches one annotation; an exact full id always wins.
Several matches are refused with the full ids listed, and no match is a not
found error. An id with unusual characters prints escaped in mtix show, so copy
it from mtix show --json instead.`,
		Args: cobra.ExactArgs(2),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runResolveAnnotation(args[0], args[1])
		}),
	}
}

func runResolveAnnotation(nodeID, annotID string) error {
	if app.promptSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := mutationContext()
	// The service resolves a short prefix to the full id and reports it.
	fullID, err := app.promptSvc.ResolveAnnotationRef(ctx, nodeID, annotID, true)
	if err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"node_id": nodeID, "annotation_id": fullID, "status": "resolved",
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Resolved annotation %s on %s\n", format.EventID(fullID), nodeID)
	}
	return nil
}

// newContextCmd creates the mtix context command per FR-12.2.
func newContextCmd() *cobra.Command {
	var maxTokens int
	var withChain bool

	cmd := &cobra.Command{
		Use:   "context <id>",
		Short: "Show assembled context chain for a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runContext(args[0], maxTokens, withChain)
		},
	}

	cmd.Flags().IntVar(&maxTokens, "max-tokens", 0,
		"Maximum token budget for assembled prompt (0 = unlimited)")
	cmd.Flags().BoolVar(&withChain, "chain", false,
		"With --json, add prompt, creator and annotations to each structured chain entry (default: they appear once, in assembled_prompt)")

	return cmd
}

func runContext(id string, maxTokens int, withChain bool) error {
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
		// MTIX-225: ancestor content appears once, in assembled_prompt; the
		// full structured chain is opt-in via --chain.
		if !withChain {
			return NewOutputWriter(true).WriteJSON(resp.Slim())
		}
		return NewOutputWriter(true).WriteJSON(resp)
	}
	fmt.Println("=== Context Chain ===")
	for _, entry := range resp.Chain {
		indent := ""
		for i := 0; i < entry.Depth; i++ {
			indent += "  "
		}
		fmt.Printf("%s%s [%s] %s\n", indent, entry.ID, entry.Status, format.SafeLine(entry.Title))
	}
	// The assembled prompt embeds stored titles and prompt text; print the
	// display form, in which no stored line can start a heading of its own
	// (MTIX-187.7.13). --json carries the exact text.
	if shown := format.SafeBlock(app.ctxSvc.AssembleDisplayPrompt(resp, maxTokens)); shown != "" {
		fmt.Printf("\n=== Assembled Prompt (%d chars) ===\n%s\n", utf8.RuneCountInString(shown), shown)
	}
	return nil
}
