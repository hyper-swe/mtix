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
		Short: "Check or fix sync between SQLite and tasks.json (FR-15)",
		Long: `Without subcommand: compare the SQLite database with .mtix/tasks.json and
report any drift. Use --fix to re-export the database to tasks.json,
resolving any discrepancies.

With subcommand (FR-18 / MTIX-15): manage the BYO Postgres sync hub.
See 'mtix sync init --help' and 'mtix sync clone --help'.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSync(cmd, fix)
		},
	}

	cmd.Flags().BoolVar(&fix, "fix", false,
		"Re-export database to tasks.json to resolve drift")

	// FR-18 subcommands. Each is a thin cobra wrapper around the
	// transport + reconcile data layer that landed in MTIX-15.3-15.6.
	cmd.AddCommand(
		newSyncInitCmd(),
		newSyncCloneCmd(),
	)

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

	// --fix always re-exports the database, even when the report says
	// InSync=true. This refreshes both sentinel hash files and the meta
	// last_export_hash, clearing the conflict-detection warning that
	// AutoImport otherwise emits when the sentinels are stale despite
	// content being equivalent (MTIX-11).
	if fix {
		if exportErr := app.syncSvc.AutoExport(ctx, app.mtixDir); exportErr != nil {
			return fmt.Errorf("fix: %w", exportErr)
		}
		if report.InSync {
			fmt.Println("Sentinels refreshed: tasks.json re-exported from database.")
		} else {
			fmt.Println("Sync fixed: tasks.json updated from database.")
		}
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
