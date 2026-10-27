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

// newSyncQuarantineAcknowledgeCmd exposes audited local recovery (MTIX-187.2).
func newSyncQuarantineAcknowledgeCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "acknowledge <event-id> --reason <text>",
		Short: "Record a local receipt accepting an invalid-id pulled event as unapplied",
		Long: `Acknowledge one retained pull or sweep event refused for an invalid node id.
The required reason and resolved author, UTC time and exact raw-event SHA256
are recorded in an immutable local receipt. Repeating the command returns
the first receipt, marking already_acknowledged in JSON or explaining that
the supplied reason was not recorded in text. The reason is limited to
500 UTF-8 bytes. No terminal or hub connection is required.

The event stays quarantined and unapplied; every pull still retries it.
This does not repair or remove it, change the hub or acknowledge another
replica. Push holds, unrelated failures and malformed retained events are
refused. Doctor passes its quarantine check only when no unacknowledged
pulled event remains; other checks keep their own results.`,
		Args: syncExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncQuarantineAcknowledge(cmd.Context(), cmd.OutOrStdout(), args[0], reason)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Required explanation for accepting this event as unapplied")
	if err := cmd.MarkFlagRequired("reason"); err != nil {
		// This constructor just declared the flag; failure is a programming invariant.
		panic(fmt.Errorf("mark acknowledgement reason required: %w", err))
	}
	return cmd
}

// runSyncQuarantineAcknowledge reports the first receipt and repeat status (MTIX-187.2).
func runSyncQuarantineAcknowledge(ctx context.Context, w io.Writer, eventID, reason string) error {
	if app.mtixDir == "" || app.syncSvc == nil {
		return fmt.Errorf("mtix sync quarantine acknowledge: local project store not initialized")
	}
	a, err := app.syncSvc.AcknowledgeQuarantine(ctx, eventID, app.authorID, reason)
	if err != nil {
		return fmt.Errorf("mtix sync quarantine acknowledge: %w", err)
	}
	if app.jsonOutput {
		if err = json.NewEncoder(w).Encode(a); err != nil {
			return fmt.Errorf("encode quarantine acknowledgement receipt: %w", err)
		}
		return nil
	}
	status := "acknowledged"
	if a.AlreadyAcknowledged {
		status = "already acknowledged; first receipt stands; supplied reason was not recorded; event"
	}
	_, err = fmt.Fprintf(w, "%s %s on this replica; retained, unapplied; author %s; at %s; raw SHA256 %s; reason: %s\n",
		status, oneLine(a.EventID), oneLine(a.Actor), oneLine(a.AcknowledgedAt), oneLine(a.RawEventSHA256), oneLine(a.Reason))
	if err != nil {
		return fmt.Errorf("write quarantine acknowledgement receipt: %w", err)
	}
	return nil
}

// quarantineDisplayReason appends the retained receipt without changing the original reason (MTIX-187.2).
func quarantineDisplayReason(q service.QuarantinedEvent) string {
	if q.Acknowledgement == nil {
		return q.Reason
	}
	a := q.Acknowledgement
	return fmt.Sprintf("%s; acknowledged by %s at %s; raw SHA256 %s; reason: %s", q.Reason, a.Actor, a.AcknowledgedAt, a.RawEventSHA256, a.Reason)
}
