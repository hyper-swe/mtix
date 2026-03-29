// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
)

// newSyncCmd creates the mtix sync command for diagnosing and fixing
// drift between the SQLite database and .mtix/tasks.json per FR-15.
func newSyncCmd() *cobra.Command {
	var fix bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Check or fix sync between SQLite and tasks.json",
		Long: `Compare the SQLite database with .mtix/tasks.json and report any drift.
Use --fix to re-export the database to tasks.json, resolving any discrepancies.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSync(cmd, fix)
		},
	}

	cmd.Flags().BoolVar(&fix, "fix", false,
		"Re-export database to tasks.json to resolve drift")

	return cmd
}

func runSync(cmd *cobra.Command, fix bool) error {
	if app.syncSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := cmd.Context()

	report, err := app.syncSvc.Compare(ctx, app.mtixDir)
	if err != nil {
		return fmt.Errorf("compare: %w", err)
	}

	if app.jsonOutput {
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("marshal report: %w", marshalErr)
		}
		fmt.Println(string(data))
	} else {
		printSyncReport(report)
	}

	if fix && !report.InSync {
		if exportErr := app.syncSvc.AutoExport(ctx, app.mtixDir); exportErr != nil {
			return fmt.Errorf("fix: %w", exportErr)
		}
		fmt.Println("Sync fixed: tasks.json updated from database.")
	}

	return nil
}

func printSyncReport(report *service.SyncReport) {
	if report.InSync {
		fmt.Printf("In sync: %d nodes in both SQLite and tasks.json\n",
			report.DBNodeCount)
		return
	}

	fmt.Printf("OUT OF SYNC\n")
	fmt.Printf("  SQLite:     %d nodes\n", report.DBNodeCount)
	fmt.Printf("  tasks.json: %d nodes\n", report.FileNodeCount)

	if len(report.OnlyInFile) > 0 {
		fmt.Printf("  Only in tasks.json (%d):\n", len(report.OnlyInFile))
		for _, id := range report.OnlyInFile {
			fmt.Printf("    - %s\n", id)
		}
	}
	if len(report.OnlyInDB) > 0 {
		fmt.Printf("  Only in SQLite (%d):\n", len(report.OnlyInDB))
		for _, id := range report.OnlyInDB {
			fmt.Printf("    - %s\n", id)
		}
	}
}
