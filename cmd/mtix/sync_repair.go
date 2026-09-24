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
without such events is never listed or changed. State that a change
outside the event log may have set is never repaired by --apply alone: a
block while a blocker is unresolved and a node cancelled with an ancestor
by cancel --cascade are left alone, and a difference only in the assignee
or agent_state (mtix update --assignee leaves no trace) is flagged.

Each listed node shows its winning event, when it was made, whether this
machine or another machine made it, and a reason. A replay (the stored
state is the row of an older event, as a replayed pull leaves it) and a
derived fix (the status matches; closed_at or progress does not) are
repaired by --apply. Anything else, for example newer state that arrived
by importing .mtix/tasks.json, is FLAGGED "not a replay; review" and is
repaired only with --apply --force, after review. Clocks that differ
between machines can mislead the check both ways: a genuine replay can be
flagged, and when this machine's clock is ahead, a teammate's newer state
can look like a replay, which --apply would revert. Check each replay's
winner time and origin before --apply.

Run 'mtix sync pull' before listing, and again just before --apply. A
repair event carries this machine's newest clock, so on a log that lacks a
teammate's newer change it can win over that change (whenever this
machine's Lamport clock is ahead of it), and every machine that pulls it
then reverts the teammate's change. After pulling, list again and apply
only what is still listed.

Without --apply the command is a dry run: it lists the differences and
writes nothing. When it lists differences, it ends with a reminder to pull
first. --json prints them as JSON, with that reminder in a "reminder"
field, which is omitted when there is no reminder.

With --apply it first writes a verified backup of the database to
.mtix/data/backups/pre-repair-status-<UTC time>.db, and stops if it cannot.
Then it repairs each node in its own transaction: it writes the derived
state, records an activity entry and recomputes the parent's progress.
When the status changes it also emits one status-change event (reason
"sync repair", stamped with the winning event's time, or with the current
time if that is in the future), which other machines apply at their next
pull and which fires status.changed hooks, and it unblocks dependents; a
repair that leaves the status alone emits nothing. A second run lists
nothing. Run 'mtix sync push' afterwards to send the events.`

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
	if report != nil {
		if printErr := writeStatusRepairReport(w, report); printErr != nil {
			return printErr
		}
	}
	if err != nil {
		return fmt.Errorf("mtix sync repair --status: %w", err)
	}
	return nil
}

// writeStatusRepairReport prints the report, as JSON with --json, including
// the partial report of a repair that failed part-way.
func writeStatusRepairReport(w io.Writer, report *service.StatusRepairReport) error {
	if !app.jsonOutput {
		printStatusRepairReport(w, report)
		return nil
	}
	report.Reminder = pullFirstReminder(report)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal repair report: %w", err)
	}
	if _, err := fmt.Fprintln(w, string(data)); err != nil {
		return fmt.Errorf("print repair report: %w", err)
	}
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

// syncRepairPullFirst is the reminder a dry run that lists differences ends
// with (MTIX-95.35).
const syncRepairPullFirst = "Run 'mtix sync pull' first and list again before --apply: a repair made on " +
	"a stale local log can revert a teammate's newer change on every machine."

// pullFirstReminder returns the pull-first reminder for a dry run that lists
// differences, and "" otherwise: nothing to repair, or --apply (MTIX-95.35).
// A repair event carries this machine's newest Lamport clock, so on a log
// that lacks a teammate's newer change it can win over that change (whenever
// this machine's Lamport clock is ahead of it) and revert it on every machine
// that pulls it.
func pullFirstReminder(r *service.StatusRepairReport) string {
	if r.Apply || len(r.Differences) == 0 {
		return ""
	}
	return syncRepairPullFirst
}

// printStatusRepairReport prints a status repair report for people. A dry run
// that lists differences ends with the pull-first reminder (MTIX-95.35).
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
		fmt.Fprintln(w, pullFirstReminder(r))
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
