// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"

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
resolving any discrepancies. The report also shows whether the automatic
import of a changed tasks.json is enabled (sync.auto_sync) and the last
automatic import mtix refused, with its reason.

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
		newSyncPushCmd(),
		newSyncPullCmd(),
		newSyncStatusCmd(),
		newSyncDoctorCmd(),
		newSyncConflictsCmd(),
		newSyncReconcileCmd(),
		newSyncDaemonCmd(),
		newSyncBackupCmd(),
		newSyncBackfillCmd(),
		newSyncMigrateCmd(),
		newSyncMarkRestoredCmd(),
		newSyncCollisionsCmd(),
	)

	return cmd
}

// runSync reports drift and the auto-import state (FR-15, MTIX-95.31.2)
// and, with fix, rewrites tasks.json from the database. --fix works even
// when tasks.json cannot be compared (missing, or not parseable, such as a
// board with git conflict markers): it then warns and rewrites it.
func runSync(cmd *cobra.Command, fix bool) error {
	if app.syncSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := cmd.Context()

	report, err := app.syncSvc.Compare(ctx, app.mtixDir)
	switch {
	case err != nil && !fix:
		return fmt.Errorf("compare: %w", err)
	case err != nil:
		fmt.Fprintf(os.Stderr, "warning: compare: %v; rewriting tasks.json from the database\n", err)
	case app.jsonOutput:
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("marshal report: %w", marshalErr)
		}
		fmt.Println(string(data))
	default:
		printSyncReport(report)
	}

	// --fix always re-exports the database, even when the report says
	// InSync=true. This refreshes both sentinel hash files and the meta
	// last_export_hash, clearing the conflict-detection warning that
	// AutoImport otherwise emits when the sentinels are stale despite
	// content being equivalent (MTIX-11). It rewrites tasks.json even while
	// an auto-import of it is pending, resolving that refusal (MTIX-95.31.2).
	if fix {
		if exportErr := app.syncSvc.ForceExport(ctx, app.mtixDir); exportErr != nil {
			return fmt.Errorf("fix: %w", exportErr)
		}
		if report != nil && report.InSync {
			fmt.Println("Sentinels refreshed: tasks.json re-exported from database.")
		} else {
			fmt.Println("Sync fixed: tasks.json updated from database.")
		}
	}

	return nil
}

// printSyncReport prints the drift report and the auto-import state
// (MTIX-95.31.2).
func printSyncReport(report *service.SyncReport) {
	printDrift(report)
	printAutoImportState(report.AutoImport)
}

// printAutoImportState prints whether sync.auto_sync leaves auto-import on,
// the value as configured, and the last auto-import mtix refused
// (MTIX-95.31.2).
func printAutoImportState(state service.AutoImportState) {
	switch {
	case state.SettingError != "":
		fmt.Printf("Auto-import: enabled (sync.auto_sync: %q is neither true nor false, so the default applies; "+
			"fix it with: mtix config set sync.auto_sync true|false)\n", state.Setting)
	case state.Enabled:
		fmt.Printf("Auto-import: enabled (sync.auto_sync: %s)\n", state.Setting)
	default:
		fmt.Printf("Auto-import: disabled (sync.auto_sync: %s): tasks.json is still exported, and a changed "+
			"tasks.json is imported automatically only into a store that holds no tasks\n", state.Setting)
	}
	refusal := state.LastRefusal
	if refusal == nil {
		fmt.Println("Last auto-import refusal: none")
		return
	}
	status := "no longer pending"
	switch {
	case refusal.Pending:
		status = "pending"
	case refusal.ResolvedAt != "":
		status = "resolved " + refusal.ResolvedAt
	}
	fmt.Printf("Last auto-import refusal: %s (%s): %s\n", refusal.RefusedAt, status, refusal.Reason)
}

// printDrift prints whether the node ids of SQLite and tasks.json agree.
func printDrift(report *service.SyncReport) {
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
