// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// showLongHelp lists exactly the lines `mtix show` prints (MTIX-98). Keep it in
// step with runShow: a test fails when a printed label is missing here.
const showLongHelp = `Show a node's details and annotations as labeled lines, in this order:

  ID           node id, marked when the id is still provisional
  Title        title
  Status       status with its icon
  Priority     priority (1 = critical ... 5 = backlog)
  Type         node type
  Assignee     current assignee (only when set)
  Desc         full description (only when set; omitted when it holds only
               whitespace or control characters), further lines indented
  Annotations  every annotation, oldest first: its ISO-8601 UTC timestamp
               ("unknown time" when none was recorded), author, addressee
               and resolved marker when present, then the text, with
               further lines indented and "(empty)" for an empty text;
               "Annotations: none" when the node has none
  Prompt       the prompt, cut to 100 characters ending in "..." when
               longer (only when set; omitted when it holds only
               whitespace or control characters), further lines indented
  Progress     progress bar
  Created      creation time

Annotation text, author and addressee, the description and the prompt are
normalized for the terminal: control characters other than newline and tab
are removed (so CRLF becomes LF), and leading blank lines and trailing
whitespace are trimmed, so a stored carriage return or escape sequence
cannot overwrite a line or restyle the terminal. Title and assignee still
print as stored, control characters and newlines included. Invisible
Unicode formatting characters, such as bidirectional overrides, are not
removed yet, so text can still display in a misleading order; quote exact
text from --json.

Acceptance criteria, labels, dependencies, activity and the full prompt are
not printed. Use --json for the complete node record, including every
annotation.`

// newShowCmd creates the mtix show command per FR-6.3.
func newShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a node's details and annotations",
		Long:  showLongHelp,
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
		changedSince  string
		outputFormat  string
		maxFieldChars int
		showEmpty     bool
		limit         int
		project       string
		allProjects   bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List nodes with filters",
		Aliases: []string{"ls"},
		RunE: func(_ *cobra.Command, _ []string) error {
			return runList(status, under, assignee, nodeType, priority, fields, changedSince, outputFormat, maxFieldChars, showEmpty, limit, project, allProjects)
		},
	}

	cmd.Flags().StringVar(&status, "status", "", "Filter by status (comma-separated for multiple)")
	cmd.Flags().StringVar(&under, "under", "", "Filter by parent subtree (comma-separated for multiple)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Filter by assignee (comma-separated for multiple)")
	cmd.Flags().StringVar(&nodeType, "type", "", "Filter by node type (comma-separated for multiple)")
	cmd.Flags().StringVar(&priority, "priority", "", "Filter by priority (comma-separated for multiple)")
	cmd.Flags().StringVar(&fields, "fields", "", "Restrict output to these fields (comma-separated)")
	cmd.Flags().StringVar(&changedSince, "changed-since", "", "Only nodes updated after this RFC3339 time or relative duration (e.g. 1h, 30m)")
	cmd.Flags().StringVar(&outputFormat, "format", "", "Output format: briefing")
	cmd.Flags().IntVar(&maxFieldChars, "max-field-chars", 0, "Truncate field values (briefing format)")
	cmd.Flags().BoolVar(&showEmpty, "show-empty", false, "Include empty fields (briefing format)")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum results")
	addProjectScopeFlags(cmd, &project, &allProjects)

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
	// Resolve display_path -> uid -> node so a reference survives a renumber
	// (ADR-003 §5): a plain display id is the common case, but a reference held
	// as a durable uid still resolves to the node's current path.
	node, err := resolveNodeRef(ctx, app.store, id)
	if err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)

	if app.jsonOutput {
		return out.WriteJSON(node)
	}

	icon := StatusIcon(string(node.Status))
	// A provisional id is flagged so the reader knows its number is not yet
	// settled and must not be externalized (ADR-003 §8).
	out.WriteHuman("ID:       %s\n", format.AnnotateID(node.ID))
	out.WriteHuman("Title:    %s\n", node.Title)
	out.WriteHuman("Status:   %s %s\n", icon, node.Status)
	out.WriteHuman("Priority: %d\n", node.Priority)
	out.WriteHuman("Type:     %s\n", node.NodeType)
	if node.Assignee != "" {
		out.WriteHuman("Assignee: %s\n", node.Assignee)
	}
	// Description and prompt are normalized and their continuation lines
	// indented, so no stored line can pass for a label; one that normalizes
	// to nothing is omitted like an unset one (MTIX-100.1).
	if desc := displayText(node.Description); desc != "" {
		out.WriteHuman("Desc:     %s\n", indentContinuation(desc, showValueIndent))
	}
	writeAnnotations(out, node.Annotations)
	if prompt := displayText(node.Prompt); prompt != "" {
		out.WriteHuman("Prompt:   %s\n", indentContinuation(truncateChars(prompt, 100), showValueIndent))
	}
	out.WriteHuman("Progress: %s\n", ProgressBar(node.Progress, 15))
	out.WriteHuman("Created:  %s\n", node.CreatedAt.Format("2006-01-02 15:04"))
	return nil
}

// writeAnnotations prints every annotation of a node for `mtix show`, oldest
// first, each headed by its UTC timestamp and author (FR-3.4, MTIX-98).
// Absence is stated as "Annotations: none", never left silent, so a reader can
// tell "no verdict" from "verdict not shown". Text is normalized first
// (MTIX-100.1): control characters removed, CRLF normalized, leading blank
// lines and trailing whitespace trimmed, an empty body shown as "(empty)", a
// missing timestamp as "unknown time", and continuation lines indented under
// the header.
func writeAnnotations(out OutputWriter, annotations []model.Annotation) {
	if len(annotations) == 0 {
		out.WriteHuman("Annotations: none\n")
		return
	}
	out.WriteHuman("Annotations:\n")
	for _, a := range sortedAnnotations(annotations) {
		lines := strings.Split(indentContinuation(annotationText(a.Text), annotationTextIndent), "\n")
		out.WriteHuman("  [%s] %s: %s\n", annotationTime(a.CreatedAt), annotationByline(a), lines[0])
		for _, line := range lines[1:] {
			out.WriteHuman("%s\n", line)
		}
	}
}

// annotationByline renders the author, the addressee when the annotation is
// directed at an agent, and a resolved marker. Author and addressee are
// single-line fields: a newline in either cannot start a line of its own.
func annotationByline(a model.Annotation) string {
	byline := singleLine(a.Author)
	if addressee := singleLine(a.Addressee); addressee != "" {
		byline += " → " + addressee
	}
	if a.Resolved {
		byline += " (resolved)"
	}
	return byline
}

// sortedAnnotations returns a copy ordered oldest first. The sort is stable,
// so annotations with equal timestamps keep their stored order, which is the
// order they were added in.
func sortedAnnotations(annotations []model.Annotation) []model.Annotation {
	sorted := make([]model.Annotation, len(annotations))
	copy(sorted, annotations)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
	})
	return sorted
}

// runList displays nodes with status icons and aligned columns.
// Filter values are comma-separated strings parsed via splitCSV per FR-17.1.
// The fields parameter restricts output to the specified fields per FR-17.3.
// The outputFormat parameter selects "briefing" format per FR-17.4.
func runList(status, under, assignee, nodeType, priority, fields, changedSince, outputFormat string, maxFieldChars int, showEmpty bool, limit int, project string, allProjects bool) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	priorities, err := splitCSVInts(priority)
	if err != nil {
		return fmt.Errorf("invalid --priority value: %w: %w", err, model.ErrInvalidInput)
	}

	since, err := parseChangedSince(changedSince)
	if err != nil {
		return err
	}

	scope, err := resolveProjectScope(project, allProjects)
	if err != nil {
		return err
	}

	ctx := context.Background()
	filter := store.NodeFilter{
		Under:        splitCSV(under),
		Assignee:     splitCSV(assignee),
		NodeType:     splitCSV(nodeType),
		Priority:     priorities,
		Project:      scope,
		ChangedSince: since,
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
	// Resolve display_path -> uid -> node so a uid-borne reference still
	// resolves to the node's current path (ADR-003 §5); the tree is then walked
	// from the resolved (current) display id.
	node, err := resolveNodeRef(ctx, app.store, id)
	if err != nil {
		return err
	}
	id = node.ID

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
