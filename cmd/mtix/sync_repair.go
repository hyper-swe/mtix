// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
)

// syncRepairFlags are the flags of `mtix sync repair` (MTIX-95.6).
type syncRepairFlags struct {
	status bool
	apply  bool
}

// newSyncRepairCmd creates `mtix sync repair --status` (MTIX-95.6; ADR-006
// §5.3). It is a thin wrapper around SyncService.RepairStatus and reaches the
// store only through that service. --apply writes, so it runs under
// withAutoExport and .mtix/tasks.json is re-exported afterwards; a dry run
// writes nothing and exports nothing.
func newSyncRepairCmd() *cobra.Command {
	var f syncRepairFlags
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Repair local state from the local sync event log (--status)",
		Long: `Re-derive state from this machine's local sync event log and list, or
repair, every node whose stored state differs. --status is required; it is
the only repair so far.

--status re-derives each node's workflow state from its newest well-formed
claim, unclaim, defer or status-change event, with the rule a pull
applies. It compares status, assignee, agent_state, whether closed_at is
set, and the progress of a node without children, and heals nodes that an
older mtix left reverted when a pull replayed an older event. A node
without such events is never listed or changed. State set by a change
that is not a workflow event is left as it is: a block added by a new
dependency, a descendant cancelled by cancel --cascade, and an assignee
set later with mtix update --assignee.

Without --apply the command is a dry run: it lists the differences and
writes nothing. --json prints them as JSON.

With --apply it first writes a verified backup of the database to
.mtix/data/backups/pre-repair-status-<UTC time>.db, and stops if it cannot.
Then it repairs each node in its own transaction: it writes the derived
state, records a status_change activity entry, emits one status-change
event (reason "sync repair") so other machines converge at their next
pull, recomputes the parent's progress and unblocks dependents. A second
run lists nothing. Run 'mtix sync push' afterwards to send the events.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			run := func(cmd *cobra.Command, _ []string) error {
				return runSyncRepair(cmd.Context(), cmd.OutOrStdout(), f)
			}
			if f.apply {
				return withAutoExport(run)(cmd, args)
			}
			return run(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&f.status, "status", false,
		"Re-derive workflow state (status, assignee, agent_state, closed_at, leaf progress) from the local event log")
	cmd.Flags().BoolVar(&f.apply, "apply", false,
		"Back up the database, then repair every listed node (default: dry run)")
	return cmd
}

// runSyncRepair runs `mtix sync repair` through the sync service and prints
// its report (MTIX-95.6). An error raised before any node was repaired is
// marked nothingWritten, so withAutoExport skips the export.
func runSyncRepair(ctx context.Context, w io.Writer, f syncRepairFlags) error {
	if !f.status {
		return nothingWritten(fmt.Errorf("mtix sync repair: choose what to repair: --status"))
	}
	if app.syncSvc == nil || app.mtixDir == "" {
		return nothingWritten(fmt.Errorf("mtix sync repair: not in an mtix project"))
	}
	report, err := app.syncSvc.RepairStatus(ctx, app.mtixDir, f.apply, app.authorID)
	if err != nil {
		if report == nil || len(report.Repaired) == 0 {
			return nothingWritten(fmt.Errorf("mtix sync repair --status: %w", err))
		}
		printStatusRepairReport(w, report)
		return fmt.Errorf("mtix sync repair --status: %w", err)
	}
	if app.jsonOutput {
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("marshal repair report: %w", marshalErr)
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	}
	printStatusRepairReport(w, report)
	return nil
}

// printStatusRepairReport prints a status repair report for people.
func printStatusRepairReport(w io.Writer, r *service.StatusRepairReport) {
	if len(r.Differences) == 0 {
		fmt.Fprintln(w, "No differences: every node's workflow state matches its local sync events.")
		return
	}
	if !r.Apply {
		fmt.Fprintf(w, "DRY RUN: %s differing from local sync events (re-run with --apply to repair)\n",
			plural(len(r.Differences), "node"))
		printStatusRepairDiffs(w, r.Differences)
		return
	}
	fmt.Fprintf(w, "Backup: %s (verified)\n", r.Backup)
	fmt.Fprintf(w, "Repaired %s:\n", plural(len(r.Repaired), "node"))
	printStatusRepairDiffs(w, r.Repaired)
	fmt.Fprintln(w, "Run 'mtix sync push' to send the repair events.")
}

// printStatusRepairDiffs prints each node's differing columns, NULL for none.
func printStatusRepairDiffs(w io.Writer, diffs []service.StatusRepairDiff) {
	for _, d := range diffs {
		fmt.Fprintf(w, "%s  (winner: %s %s)\n", d.NodeID, d.WinnerOp, d.WinnerEventID)
		for _, c := range d.Columns {
			fmt.Fprintf(w, "  %s: %s -> %s\n", c.Column, nullText(c.Current), nullText(c.Expected))
		}
	}
}

// nullText renders a nullable column value.
func nullText(v *string) string {
	if v == nil {
		return "NULL"
	}
	return *v
}

// plural renders "1 node" or "2 nodes".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
