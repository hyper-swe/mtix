// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
)

// newSyncQuarantineCmd creates `mtix sync quarantine`, the read-only view of
// the pulled events `mtix sync pull` holds in the local quarantine
// (MTIX-95.11) and of the own events `mtix sync push` holds (MTIX-95.12).
func newSyncQuarantineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quarantine",
		Short: "Inspect events held in the local quarantine",
		Long: `Inspect the events held in the local quarantine: pulled events that
'mtix sync pull' could not take (they failed the pull's checks, the FR-18.7
caps and the sync.max_lamport_jump bound, or their apply), and events of
this replica that 'mtix sync push' holds because the hub would refuse them
(such as a field over the 64 KB sync limit). Pulled events are not applied;
every pull retries them. Held push events are not pushed. A hold for the
clock is released automatically once the event's stamp is within 24 h of
this machine's clock, together with the events of its task's subtree
that the hub would accept; every other push hold stays.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newSyncQuarantineListCmd())
	return cmd
}

// newSyncQuarantineListCmd creates `mtix sync quarantine list`.
func newSyncQuarantineListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List quarantined and held events (read-only)",
		Long: `List the events held in the local quarantine, in Lamport clock order,
then event id: event id, node, op, failed attempts, first seen, last
attempt and the reason the event was quarantined. --json adds the Lamport
clock, the source (pull or sweep for a pulled event, push for an event
push holds) and the mtix version that recorded it.

Read-only and local: it never retries, removes or changes an event and
does not contact the hub. 'mtix sync pull' retries every quarantined
pulled event, first before it contacts the hub. Push releases a held push
event automatically only in two cases: a reason that starts with
"temporary: clock:" once the event's stamp is within 24 h of this
machine's clock, and one that starts with "depends on held create of"
once no creation above it (for a link, made before it) is held and the
hub would accept it (if not, it stays held under its own reason). Every other hold stays. 'mtix sync
doctor' names the fix for each held push event.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSyncQuarantineList(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

// runSyncQuarantineList prints the quarantine through the service layer
// (SyncService.ListQuarantined), as a table or, with --json, as an array
// (`[]` when empty). Text that comes from hub rows is printed on one line
// without control characters.
func runSyncQuarantineList(ctx context.Context, stdout io.Writer) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync quarantine list: not in an mtix project (run 'mtix init' first)")
	}
	if app.syncSvc == nil {
		return fmt.Errorf("mtix sync quarantine list: local store not initialized")
	}
	events, err := app.syncSvc.ListQuarantined(ctx)
	if err != nil {
		return fmt.Errorf("mtix sync quarantine list: %w", err)
	}
	if app.jsonOutput {
		body, err := json.MarshalIndent(events, "", "  ")
		if err != nil {
			return fmt.Errorf("mtix sync quarantine list: encode: %w", err)
		}
		_, err = fmt.Fprintln(stdout, string(body))
		return err
	}
	return printQuarantineTable(stdout, events)
}

// printQuarantineTable writes one row per quarantined event, or a line
// saying there are none.
func printQuarantineTable(w io.Writer, events []service.QuarantinedEvent) error {
	if len(events) == 0 {
		_, err := fmt.Fprintln(w, "no quarantined events")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EVENT ID\tNODE\tOP\tATTEMPTS\tFIRST SEEN\tLAST ATTEMPT\tREASON")
	for _, q := range events {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			oneLine(q.EventID), oneLine(q.NodeID), oneLine(q.OpType), strconv.Itoa(q.Attempts),
			oneLine(q.FirstSeen), oneLine(q.LastAttempt), oneLine(q.Reason))
	}
	return tw.Flush()
}
