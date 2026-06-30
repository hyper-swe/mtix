// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// addProjectScopeFlags registers the shared multi-project scope flags on a
// list-style command per FR-MULTI-PROJECT MP-7: --project scopes to one
// project, --all-projects spans all. With neither, the command defaults to
// the primary project (see resolveProjectScope).
func addProjectScopeFlags(cmd *cobra.Command, project *string, allProjects *bool) {
	cmd.Flags().StringVar(project, "project", "", "Scope to a single project prefix (default: primary)")
	cmd.Flags().BoolVar(allProjects, "all-projects", false, "Span all projects")
}

// resolveProjectScope maps the --project/--all-projects flags to a
// store.NodeFilter.Project value per FR-MULTI-PROJECT MP-7. The store treats
// an empty Project as "all projects"; the policy of defaulting a scope-less
// query to the primary project lives here in the CLI, not in the store:
//   - --all-projects        -> "" (span all)
//   - --project X           -> X
//   - neither flag          -> the primary project (config "prefix")
//
// The two flags are mutually exclusive.
func resolveProjectScope(project string, allProjects bool) (string, error) {
	if allProjects && project != "" {
		return "", fmt.Errorf("--project and --all-projects are mutually exclusive: %w", model.ErrInvalidInput)
	}
	if allProjects {
		return "", nil
	}
	if project != "" {
		return project, nil
	}
	// Neither flag: default to the primary project. When config is unavailable
	// (e.g. an uninitialized project), fall back to spanning all.
	if app.configSvc == nil {
		return "", nil
	}
	return app.configSvc.Get("prefix")
}

// newSearchCmd creates the mtix search command per FR-6.3 / FR-17.1.
// All filter flags accept comma-separated multiple values.
func newSearchCmd() *cobra.Command {
	var (
		status      string
		assignee    string
		nodeType    string
		under       string
		priority    string
		fields      string
		limit       int
		project     string
		allProjects bool
	)

	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search nodes with advanced filters",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runSearch(status, assignee, nodeType, under, priority, fields, limit, project, allProjects)
		},
	}

	cmd.Flags().StringVar(&status, "status", "", "Filter by status (comma-separated for multiple)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Filter by assignee (comma-separated for multiple)")
	cmd.Flags().StringVar(&nodeType, "type", "", "Filter by node type (comma-separated for multiple)")
	cmd.Flags().StringVar(&under, "under", "", "Filter by parent subtree (comma-separated for multiple)")
	cmd.Flags().StringVar(&priority, "priority", "", "Filter by priority (comma-separated, 1-5)")
	cmd.Flags().StringVar(&fields, "fields", "", "Restrict JSON output to these fields (comma-separated)")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum results")
	addProjectScopeFlags(cmd, &project, &allProjects)

	return cmd
}

func runSearch(status, assignee, nodeType, under, priority, fields string, limit int, project string, allProjects bool) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	priorities, err := splitCSVInts(priority)
	if err != nil {
		return fmt.Errorf("invalid --priority value: %w: %w", err, model.ErrInvalidInput)
	}

	scope, err := resolveProjectScope(project, allProjects)
	if err != nil {
		return err
	}

	filter := store.NodeFilter{
		Assignee: splitCSV(assignee),
		NodeType: splitCSV(nodeType),
		Under:    splitCSV(under),
		Priority: priorities,
		Project:  scope,
	}
	for _, s := range splitCSV(status) {
		filter.Status = append(filter.Status, model.Status(s))
	}

	ctx := context.Background()
	nodes, total, err := app.store.ListNodes(ctx, filter, store.ListOptions{Limit: limit})
	if err != nil {
		return err
	}

	return printNodeList(nodes, total, splitCSV(fields))
}

// newReadyCmd creates the mtix ready command per FR-6.3.
func newReadyCmd() *cobra.Command {
	var (
		project     string
		allProjects bool
	)
	cmd := &cobra.Command{
		Use:   "ready",
		Short: "List nodes ready for work (unblocked, unassigned)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runReady(project, allProjects)
		},
	}
	addProjectScopeFlags(cmd, &project, &allProjects)
	return cmd
}

func runReady(project string, allProjects bool) error {
	if app.bgSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	scope, err := resolveProjectScope(project, allProjects)
	if err != nil {
		return err
	}

	ctx := context.Background()
	nodes, err := app.bgSvc.GetReadyNodes(ctx)
	if err != nil {
		return err
	}

	// GetReadyNodes spans all projects; scope the result in the CLI per MP-7.
	nodes = filterNodesByProject(nodes, scope)
	return printNodeList(nodes, len(nodes), nil)
}

// filterNodesByProject keeps only nodes in the given project. An empty scope
// means "all projects" and returns the input unchanged (FR-MULTI-PROJECT MP-7).
func filterNodesByProject(nodes []*model.Node, scope string) []*model.Node {
	if scope == "" {
		return nodes
	}
	filtered := make([]*model.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Project == scope {
			filtered = append(filtered, n)
		}
	}
	return filtered
}

// newBlockedCmd creates the mtix blocked command per FR-6.3.
func newBlockedCmd() *cobra.Command {
	var (
		project     string
		allProjects bool
	)
	cmd := &cobra.Command{
		Use:   "blocked",
		Short: "List nodes blocked by dependencies",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runBlocked(project, allProjects)
		},
	}
	addProjectScopeFlags(cmd, &project, &allProjects)
	return cmd
}

func runBlocked(project string, allProjects bool) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	scope, err := resolveProjectScope(project, allProjects)
	if err != nil {
		return err
	}

	filter := store.NodeFilter{
		Status:  []model.Status{model.StatusBlocked},
		Project: scope,
	}

	ctx := context.Background()
	nodes, total, err := app.store.ListNodes(ctx, filter, store.ListOptions{Limit: 50})
	if err != nil {
		return err
	}

	return printNodeList(nodes, total, nil)
}

// newStaleCmd creates the mtix stale command per FR-6.3.
func newStaleCmd() *cobra.Command {
	var (
		project     string
		allProjects bool
	)
	cmd := &cobra.Command{
		Use:   "stale",
		Short: "List nodes with stale agent assignments",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runStale(project, allProjects)
		},
	}
	addProjectScopeFlags(cmd, &project, &allProjects)
	return cmd
}

func runStale(project string, allProjects bool) error {
	if app.agentSvc == nil || app.configSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	// Validate the scope flags for symmetry with the other list-style commands
	// (MP-7). Agent heartbeat staleness is project-agnostic — an agent is not
	// tied to a project — so the resolved scope does not filter the agent list.
	if _, err := resolveProjectScope(project, allProjects); err != nil {
		return err
	}

	threshold := app.configSvc.AgentStaleThreshold()
	ctx := context.Background()
	agents, err := app.agentSvc.GetStaleAgents(ctx, threshold)
	if err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)

	if app.jsonOutput {
		return out.WriteJSON(agents)
	}

	if len(agents) == 0 {
		out.WriteHuman("No stale agents found\n")
	} else {
		out.WriteHuman("Stale agents:\n")
		for _, a := range agents {
			out.WriteHuman("  %s\n", a)
		}
	}
	return nil
}

// newOrphansCmd creates the mtix orphans command per FR-6.3.
func newOrphansCmd() *cobra.Command {
	var (
		project     string
		allProjects bool
	)
	cmd := &cobra.Command{
		Use:   "orphans",
		Short: "List root-level nodes (no parent)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runOrphans(project, allProjects)
		},
	}
	addProjectScopeFlags(cmd, &project, &allProjects)
	return cmd
}

func runOrphans(project string, allProjects bool) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	scope, err := resolveProjectScope(project, allProjects)
	if err != nil {
		return err
	}

	// Root nodes have no parent — list with empty under filter.
	filter := store.NodeFilter{Project: scope}
	ctx := context.Background()
	nodes, total, err := app.store.ListNodes(ctx, filter, store.ListOptions{Limit: 100})
	if err != nil {
		return err
	}

	// Filter to only root nodes (empty parent_id).
	var roots []*model.Node
	for _, n := range nodes {
		if n.ParentID == "" {
			roots = append(roots, n)
		}
	}

	return printNodeList(roots, total, nil)
}

// printNodeList formats and prints a list of nodes using OutputWriter with status icons.
// Applies natural dot-notation sort per FR-17.6 before rendering.
// When fields is non-nil and JSON mode is active, output is projected to
// only the requested fields per FR-17.3.
func printNodeList(nodes []*model.Node, total int, fields []string) error {
	// Sort by natural dot-notation ID order per FR-17.6.
	format.SortNodes(nodes)

	out := NewOutputWriter(app.jsonOutput)

	if app.jsonOutput {
		if len(fields) > 0 {
			projected, err := format.ProjectNodes(nodes, fields)
			if err != nil {
				return err
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
