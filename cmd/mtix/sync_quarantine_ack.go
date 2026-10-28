// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/service"
)

// newSyncQuarantineAcknowledgeCmd exposes audited local recovery (MTIX-187.2).
func newSyncQuarantineAcknowledgeCmd() *cobra.Command {
	var reason, handle string
	cmd := &cobra.Command{
		Use:   "acknowledge (<event-id> | --handle <handle>) --reason <text>",
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
pulled event remains; other checks keep their own results.

Name the event by its id, or by --handle: a prefix of the SHA-256 of the exact
id bytes, printed as at least 12 hex characters (longer when two retained
events share the shorter prefix). 8 hex characters are accepted when
unambiguous; an ambiguous prefix is refused with the length that separates the
events. 'mtix sync quarantine list' and 'mtix sync doctor' print the handle. Use
the handle when the id holds NUL, invalid UTF-8 or characters that cannot be typed.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if handle != "" {
				return syncExactArgs(0)(cmd, args)
			}
			return syncExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if handle != "" {
				return runSyncQuarantineAcknowledgeHandle(cmd.Context(), cmd.OutOrStdout(), handle, reason)
			}
			return runSyncQuarantineAcknowledge(cmd.Context(), cmd.OutOrStdout(), args[0], reason)
		},
	}
	cmd.Flags().StringVar(&handle, "handle", "", "Event handle from 'mtix sync quarantine list' (instead of the event id)")
	cmd.Flags().StringVar(&reason, "reason", "", "Required explanation for accepting this event as unapplied")
	if err := cmd.MarkFlagRequired("reason"); err != nil {
		// This constructor just declared the flag; failure is a programming invariant.
		panic(fmt.Errorf("mark acknowledgement reason required: %w", err))
	}
	return cmd
}

// runSyncQuarantineAcknowledgeHandle resolves a handle to the one retained event
// it names and acknowledges that event exactly as the id form does (MTIX-187.7.6).
func runSyncQuarantineAcknowledgeHandle(ctx context.Context, w io.Writer, handle, reason string) error {
	if app.mtixDir == "" || app.syncSvc == nil {
		return fmt.Errorf("mtix sync quarantine acknowledge: local project store not initialized")
	}
	eventID, err := app.syncSvc.ResolveQuarantineHandle(ctx, handle)
	if err != nil {
		return fmt.Errorf("mtix sync quarantine acknowledge: %w", err)
	}
	return runSyncQuarantineAcknowledge(ctx, w, eventID, reason)
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
		status, format.EventID(a.EventID), oneLine(a.Actor), oneLine(a.AcknowledgedAt), oneLine(a.RawEventSHA256), oneLine(a.Reason))
	if err != nil {
		return fmt.Errorf("write quarantine acknowledgement receipt: %w", err)
	}
	return nil
}

// quarantineDisplayReason appends the retained receipt without changing the original reason (MTIX-187.2).
func quarantineDisplayReason(q service.QuarantinedEvent) string {
	if q.Acknowledgement == nil {
		return displayHoldReason(q.Reason)
	}
	a := q.Acknowledgement
	return fmt.Sprintf("%s; acknowledged by %s at %s; raw SHA256 %s; reason: %s", displayHoldReason(q.Reason), a.Actor, a.AcknowledgedAt, a.RawEventSHA256, a.Reason)
}
