// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// newProjectsCmd creates the mtix projects command per FR-MULTI-PROJECT MP-8.
// It lists the distinct projects present in the DB with their node counts,
// marking the primary project (config "prefix").
func newProjectsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "projects",
		Short: "List projects in the database with node counts",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runProjects()
		},
	}
}

// runProjects lists distinct projects with per-project node counts, marking
// the primary (FR-MULTI-PROJECT MP-8). The store reports projects derived from
// node data; the primary flag is overlaid here from config (the store is
// deliberately unaware of which project is primary).
func runProjects() error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	infos, err := app.store.DistinctProjects(ctx)
	if err != nil {
		return err
	}

	// Resolve the primary project from config; absent config, nothing is marked.
	var primary string
	if app.configSvc != nil {
		primary, _ = app.configSvc.Get("prefix")
	}

	out := NewOutputWriter(app.jsonOutput)

	if app.jsonOutput {
		type projectOut struct {
			Prefix    string `json:"prefix"`
			Count     int    `json:"count"`
			IsPrimary bool   `json:"is_primary"`
		}
		projects := make([]projectOut, 0, len(infos))
		for _, info := range infos {
			projects = append(projects, projectOut{
				Prefix:    info.Prefix,
				Count:     info.Count,
				IsPrimary: info.Prefix == primary,
			})
		}
		return out.WriteJSON(map[string]any{"projects": projects})
	}

	headers := []string{"Project", "Nodes", "Primary"}
	rows := make([][]string, 0, len(infos))
	for _, info := range infos {
		marker := ""
		if info.Prefix == primary {
			marker = "*"
		}
		rows = append(rows, []string{
			info.Prefix,
			fmt.Sprintf("%d", info.Count),
			marker,
		})
	}
	out.WriteTable(headers, rows)
	return nil
}
