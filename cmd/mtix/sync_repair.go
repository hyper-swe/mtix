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
	force  bool
}

// syncRepairLong is the help text of `mtix sync repair` (MTIX-95.6); it is
// reproduced in docs/CLI_REFERENCE.md.
const syncRepairLong = `Re-derive state from this machine's local sync event log and list, or
repair, every node whose stored state differs. --status is required; it is
the only repair so far.

--status re-derives each node's workflow state from its newest well-formed
claim, unclaim, defer or status-change event, with the rule a pull
applies. It compares status, assignee, agent_state, whether closed_at is
set, and the progress of a node without children, to heal nodes that an
older mtix left reverted when a pull replayed an older event. A node
without such events is never listed or changed. State set by a change
that is not a workflow event is left as it is: a block while a blocker
is unresolved, a node cancelled with its parent by cancel --cascade, and
an assignee set later with mtix update --assignee.

Each listed node shows its winning event and when it was made, and a
reason. A replay (the stored state is the row of an older event, as a
replayed pull leaves it) and a derived fix (the status matches; closed_at
or progress does not) are repaired by --apply. Anything else, for example
newer state that arrived by importing .mtix/tasks.json, is FLAGGED "not a
replay; review" and is repaired only with --apply --force, after review.

Without --apply the command is a dry run: it lists the differences and
writes nothing. --json prints them as JSON.

With --apply it first writes a verified backup of the database to
.mtix/data/backups/pre-repair-status-<UTC time>.db, and stops if it cannot.
Then it repairs each node in its own transaction: it writes the derived
state, records an activity entry and recomputes the parent's progress.
When the status changes it also emits one status-change event (reason
"sync repair", stamped with the winning event's time), which other
machines apply at their next pull and which fires status.changed hooks,
and it unblocks dependents; a repair that leaves the status alone emits
nothing. A second run lists nothing. Run 'mtix sync push' afterwards to
send the events.`

// newSyncRepairCmd creates `mtix sync repair --status` (MTIX-95.6; ADR-006
// §5.3). It is a thin wrapper around SyncService.RepairStatus and reaches the
// store only through that service. Only a run that repaired a node writes, so
// only such a run re-exports .mtix/tasks.json (runSyncRepair).
func newSyncRepairCmd() *cobra.Command {
	var f syncRepairFlags
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Repair local state from the local sync event log (--status)",
		Long:  syncRepairLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSyncRepair(cmd.Context(), cmd.OutOrStdout(), f)
		},
	}
	cmd.Flags().BoolVar(&f.status, "status", false,
		"Re-derive workflow state (status, assignee, agent_state, closed_at, leaf progress) from the local event log")
	cmd.Flags().BoolVar(&f.apply, "apply", false,
		"Back up the database, then repair every listed node that is not flagged (default: dry run)")
	cmd.Flags().BoolVar(&f.force, "force", false,
		"With --apply, also repair nodes flagged for review (not a replay)")
	return cmd
}

// runSyncRepair runs `mtix sync repair` through the sync service and prints
// its report (MTIX-95.6). .mtix/tasks.json is re-exported only when a node
// was repaired, also when a later node failed; a dry run, a refused run and
// an --apply with nothing to repair write nothing and export nothing.
func runSyncRepair(ctx context.Context, w io.Writer, f syncRepairFlags) error {
	switch {
	case !f.status:
		return fmt.Errorf("mtix sync repair: choose what to repair: --status")
	case app.syncSvc == nil || app.mtixDir == "":
		return fmt.Errorf("mtix sync repair: not in an mtix project")
	}
	report, err := app.syncSvc.RepairStatus(ctx, app.mtixDir,
		service.StatusRepairOptions{Apply: f.apply, Force: f.force}, app.authorID)
	if report != nil && len(report.Repaired) > 0 {
		exportAfterRepair(ctx)
	}
	if err != nil {
		if report != nil {
			printStatusRepairReport(w, report)
		}
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

// exportAfterRepair re-exports .mtix/tasks.json after a repair wrote, as
// withAutoExport does for other writing commands: an export failure is
// logged and does not fail the command (FR-15.3b).
func exportAfterRepair(ctx context.Context) {
	if err := app.syncSvc.AutoExport(ctx, app.mtixDir); err != nil && app.logger != nil {
		app.logger.Warn("auto-export failed", "error", err)
	}
}

// printStatusRepairReport prints a status repair report for people.
func printStatusRepairReport(w io.Writer, r *service.StatusRepairReport) {
	if len(r.Differences) == 0 {
		fmt.Fprintln(w, "No differences: every node's workflow state matches its local sync events.")
		return
	}
	if !r.Apply {
		fmt.Fprintf(w, "DRY RUN: %s differing from local sync events, %d flagged for review.\n",
			plural(len(r.Differences), "node"), countFlagged(r.Differences))
		fmt.Fprintln(w, "Re-run with --apply to repair the others; a flagged node is repaired only with --apply --force, after review.")
		printStatusRepairDiffs(w, r.Differences)
		return
	}
	if r.Backup != "" {
		fmt.Fprintf(w, "Backup: %s (verified)\n", r.Backup)
	}
	fmt.Fprintf(w, "Repaired %s:\n", plural(len(r.Repaired), "node"))
	printStatusRepairDiffs(w, r.Repaired)
	if len(r.Skipped) > 0 {
		fmt.Fprintf(w, "Skipped %s (review, then re-run with --apply --force):\n", plural(len(r.Skipped), "flagged node"))
		printStatusRepairDiffs(w, r.Skipped)
	}
	if len(r.Repaired) > 0 {
		fmt.Fprintln(w, "Run 'mtix sync push' to send the repair events.")
	}
}

// printStatusRepairDiffs prints each node with its reason, winning event,
// replayed event and differing columns, NULL for none.
func printStatusRepairDiffs(w io.Writer, diffs []service.StatusRepairDiff) {
	for _, d := range diffs {
		marker := ""
		if d.Flagged {
			marker = "FLAGGED  "
		}
		origin := "another machine"
		if d.WinnerLocal {
			origin = "this machine"
		}
		fmt.Fprintf(w, "%s  %s%s\n", d.NodeID, marker, d.Reason)
		fmt.Fprintf(w, "  winner: %s %s at %s (%s)\n", d.WinnerOp, d.WinnerEventID, d.WinnerWallClock, origin)
		if d.MatchedEventID != "" {
			fmt.Fprintf(w, "  replayed event: %s\n", d.MatchedEventID)
		}
		for _, c := range d.Columns {
			fmt.Fprintf(w, "  %s: %s -> %s\n", c.Column, nullText(c.Current), nullText(c.Expected))
		}
	}
}

// countFlagged counts the flagged differences.
func countFlagged(diffs []service.StatusRepairDiff) int {
	n := 0
	for _, d := range diffs {
		if d.Flagged {
			n++
		}
	}
	return n
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
