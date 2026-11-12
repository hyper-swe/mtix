// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
)

// boardOptions carries the mtix board flags (MTIX-255.1). Filter values are
// comma-separated strings parsed via splitCSV; labels is the any-of filter.
type boardOptions struct {
	under, labels, assignee, project string
	limit                            int
	allProjects                      bool
}

// newBoardCmd creates the read-only mtix board command (MTIX-255.1). Columns
// are the statuses; with --json it emits {columns:[{name,count,items}]}.
func newBoardCmd() *cobra.Command {
	var o boardOptions

	cmd := &cobra.Command{
		Use:   "board",
		Args:  cobra.NoArgs,
		Short: "Show a read-only board of nodes grouped by status column",
		Long: `Show nodes grouped into one column per status (open, in_progress, blocked,
deferred, done, cancelled, invalidated) with a count per column. Read-only.
--limit caps the items shown per column; the count is always the full total.
Known limit: each column is a separate query, not one snapshot, so a node changed
while the board is built can show in two columns or in none; rerun if exact numbers matter.
With --json the output is {"columns":[{"name","count","items":[{"id","title","priority","assignee"}]}]}.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runBoardCmd(o)
		},
	}

	cmd.Flags().StringVar(&o.under, "under", "", "Only nodes under these parents (comma-separated)")
	cmd.Flags().StringVar(&o.labels, "labels", "", "Filter by label, any-of (comma-separated; exact, case-sensitive, e.g. desk:now,desk:backlog)")
	cmd.Flags().StringVar(&o.assignee, "assignee", "", "Filter by assignee (comma-separated for multiple)")
	cmd.Flags().IntVar(&o.limit, "limit", 20, "Maximum items shown per column (at least 1; counts are unaffected)")
	addProjectScopeFlags(cmd, &o.project, &o.allProjects)

	return cmd
}

// runBoardCmd builds the board through the service layer and renders it.
func runBoardCmd(o boardOptions) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}
	scope, err := resolveProjectScope(o.project, o.allProjects)
	if err != nil {
		return err
	}
	board, err := app.nodeSvc.Board(context.Background(), service.BoardRequest{
		Filter: service.NodeFilter{
			Under:     splitCSV(o.under),
			LabelsAny: splitCSV(o.labels),
			Assignee:  splitCSV(o.assignee),
			Project:   scope,
		},
		Limit: o.limit,
	})
	if err != nil {
		return err
	}
	if app.jsonOutput {
		return NewOutputWriter(true).WriteJSON(board)
	}
	return renderBoardTable(board, consoleIsUTF8())
}
