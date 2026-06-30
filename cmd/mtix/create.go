// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// createInput is the reader the new-project confirmation prompt reads from.
// It defaults to stdin and is swapped in tests to drive the [y/N] guardrail.
var createInput io.Reader = os.Stdin

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
		project     string
		yes         bool
	)

	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a new node",
		Long:  `Create a new node. Use --under to create a child node.`,
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runCreateWithProject(args[0], under, nodeType, priority,
				description, prompt, acceptance, labels, assign, project, yes)
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
	cmd.Flags().StringVar(&project, "project", "",
		"Project prefix for a root node (overrides the primary; inherited for children) (FR-MULTI-PROJECT MP-5)")
	cmd.Flags().BoolVar(&yes, "yes", false,
		"Skip the confirmation prompt when --project names a new project")

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

// runCreate is the legacy entry point retained for the micro command and the
// existing call sites: no explicit --project (resolve to the primary) and skip
// the new-project confirmation (the guardrail is only meaningful from the
// interactive create command).
func runCreate(title, under, nodeType string, priority int,
	description, prompt, acceptance, labels, assign string,
) error {
	return runCreateWithProject(title, under, nodeType, priority,
		description, prompt, acceptance, labels, assign, "", true)
}

// runCreateWithProject creates a node, resolving its project per
// FR-MULTI-PROJECT MP-5/MP-6:
//   - ROOT (no --under): the project is projectFlag if given, else the
//     configured primary. A root naming a project not already in the DB is
//     confirmed on stdin unless yes is set (MP-6 guardrail).
//   - CHILD (--under): the project is INHERITED from the parent; projectFlag,
//     if given, MUST match the parent's project — a node is never filed into a
//     different project than its parent.
func runCreateWithProject(title, under, nodeType string, priority int,
	description, prompt, acceptance, labels, assign, projectFlag string, yes bool,
) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}

	ctx := context.Background()

	project, err := resolveCreateProject(ctx, under, projectFlag, yes)
	if err != nil {
		return err
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

	node, err := app.nodeSvc.CreateNode(ctx, req)
	if err != nil {
		return err
	}

	// Warn on stderr if the task is missing fields the context chain depends on.
	// stderr keeps the warning out of --json stdout so programmatic consumers
	// see the JSON node and humans still see the warning.
	if warning := contextWarning(node.ID, prompt, acceptance); warning != "" {
		fmt.Fprint(os.Stderr, warning)
	}

	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(node)
	}
	out.WriteHuman("○ Created %s: %s\n", node.ID, node.Title)
	return nil
}

// resolveCreateProject determines the project a new node is filed into per
// FR-MULTI-PROJECT MP-5/MP-6. See runCreateWithProject for the root/child rules.
func resolveCreateProject(ctx context.Context, under, projectFlag string, yes bool) (string, error) {
	if under != "" {
		// CHILD: inherit from the parent; --project, if given, must match.
		parent, err := app.nodeSvc.GetNode(ctx, under)
		if err != nil {
			return "", fmt.Errorf("look up parent %s: %w", under, err)
		}
		if projectFlag != "" && projectFlag != parent.Project {
			return "", fmt.Errorf(
				"--project %q conflicts with parent %s in project %q: a child is always filed into its parent's project",
				projectFlag, under, parent.Project)
		}
		return parent.Project, nil
	}

	// ROOT: --project overrides the configured primary.
	project := projectFlag
	if project == "" {
		project = "PROJ"
		if app.configSvc != nil {
			if v, err := app.configSvc.Get("prefix"); err == nil {
				project = v
			}
		}
	}
	if err := model.ValidatePrefix(project); err != nil {
		return "", err
	}

	// MP-6 guardrail: confirm an unknown project unless --yes.
	if !yes {
		known, err := projectExists(ctx, project)
		if err != nil {
			return "", err
		}
		if !known && !confirmNewProject(project) {
			return "", fmt.Errorf("aborted: project %q not created", project)
		}
	}
	return project, nil
}

// projectExists reports whether prefix is already present in the DB.
func projectExists(ctx context.Context, prefix string) (bool, error) {
	projects, err := app.store.DistinctProjects(ctx)
	if err != nil {
		return false, fmt.Errorf("list projects: %w", err)
	}
	for _, p := range projects {
		if p.Prefix == prefix {
			return true, nil
		}
	}
	return false, nil
}

// confirmNewProject prompts on stdin (per MP-6) and returns true only on an
// affirmative answer. Reads from createInput so tests can drive the guardrail.
func confirmNewProject(prefix string) bool {
	fmt.Fprintf(os.Stdout, "Create new project %s? [y/N] ", prefix)
	line, _ := bufio.NewReader(createInput).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// contextWarning returns the stderr warning text for a newly created node when
// prompt or acceptance is empty. Returns "" when both fields are populated.
//
// Warn-only by design: blocking on missing fields would break legitimate
// corner cases (drafting, intentional empty placeholders during decomposition).
func contextWarning(nodeID, prompt, acceptance string) string {
	missingPrompt := prompt == ""
	missingAcceptance := acceptance == ""
	if !missingPrompt && !missingAcceptance {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n⚠ Task ")
	b.WriteString(nodeID)
	b.WriteString(" is missing context fields needed for autonomous agent pickup:\n")
	if missingPrompt {
		b.WriteString("    --prompt:     capture the originating conversation — user's ask,\n")
		b.WriteString("                  file paths/symbols already discussed, constraints,\n")
		b.WriteString("                  pointers to project skills (CLAUDE.md / AGENTS.md)\n")
	}
	if missingAcceptance {
		b.WriteString("    --acceptance: testable criteria that define \"done\"\n")
	}
	b.WriteString("\n  Completeness test: can a different agent, with zero conversation\n")
	b.WriteString("  history, execute this task using ONLY what's in the ticket?\n\n")
	b.WriteString("  Populate with:\n")
	b.WriteString("    mtix update ")
	b.WriteString(nodeID)
	if missingPrompt {
		b.WriteString(" --prompt \"...\"")
	}
	if missingAcceptance {
		b.WriteString(" --acceptance \"...\"")
	}
	b.WriteString("\n")
	return b.String()
}
