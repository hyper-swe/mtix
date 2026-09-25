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
// (MTIX-95.11).
func newSyncQuarantineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quarantine",
		Short: "Inspect pulled events held in the local quarantine",
		Long: `Inspect the pulled events that 'mtix sync pull' holds in the local
quarantine: events that failed the pull's checks (the FR-18.7 caps, the
sync.max_lamport_jump bound) or their apply. They are not applied; every
pull retries them.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newSyncQuarantineListCmd())
	return cmd
}

// newSyncQuarantineListCmd creates `mtix sync quarantine list`.
func newSyncQuarantineListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List quarantined pulled events (read-only)",
		Long: `List the pulled events held in the local quarantine, in the order the
pull retries them (Lamport clock, then event id): event id, node, op,
failed attempts, first seen, last attempt and the reason the event was
quarantined. --json adds the Lamport clock, the pass that quarantined the
event (pull or sweep) and the mtix version that did.

Read-only and local: it never retries, removes or changes an event and
does not contact the hub. 'mtix sync pull' retries every quarantined
event, first before it contacts the hub.`,
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
