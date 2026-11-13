// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
)

// newSyncQuarantineResolveCmd creates `mtix sync quarantine resolve`, the way
// out for a record relay ingest skipped (MTIX-268).
func newSyncQuarantineResolveCmd() *cobra.Command {
	var handle, reason string
	var accept, recreate bool
	cmd := &cobra.Command{
		Use:   "resolve --handle <handle> (--accept | --recreate) --reason <text>",
		Short: "Settle a record relay ingest skipped: recreate it under a new id, or accept it as unapplied",
		Long: `Settle one record that relay ingest skipped (source relay in 'mtix sync
quarantine list'). Relay ingest skips a record from an authenticated peer when
it cannot be applied, for example when two peers took the same next task number.
The record is stored, and 'mtix doctor' fails until every such record is settled.

--recreate (only for a skipped create_node record) creates a NEW task with the
skipped task's title, description, prompt, acceptance, priority, labels, type and
parent, under the next free id, and marks the record resolved in the same
transaction as the create: both happen or neither does. The new task syncs as
local work to EVERY peer, including the author peer, which still holds the
original under its own id and so holds BOTH copies. On the author peer close one
of the two: mtix cancel <id> --reason "duplicate of <other-id>" (this command
prints the new id; 'mtix sync quarantine list --json' keeps it as recreated_as).
If the skipped task's parent was itself skipped, resolve the parent first; a
child is refused while its parent is open or was accepted, and as ambiguous when
another peer also had a create of the parent's id skipped. A second recreate of an
event already recreated (a republish skipped it again) is refused; accept it. Later events the
other peer sent for its own copy are separate records; settle each one
(--accept after reading it). They are not remapped.

--accept records that you reviewed the record and keep it unapplied. Its content
stays stored, and only in this project's store, so keep that store; read it with
'mtix sync quarantine list --json' (the relay object holds the payload). Use it
when the author peer still holds the task and nobody here needs it. Nothing is
applied and no task changes.

--reason is required (at most 500 bytes). A settled record stays listed as the
audit trail. Local only: it never contacts the hub or the relay.`,
		Args: syncExactArgs(0), // a DSN-shaped argument is refused without being echoed (FR-18.17)
		RunE: func(cmd *cobra.Command, _ []string) error {
			mode, err := relayResolveMode(accept, recreate)
			if err != nil {
				return err
			}
			return runSyncQuarantineResolve(cmd.Context(), cmd.OutOrStdout(), service.RelayResolveRequest{
				Handle: handle, Mode: mode, Actor: app.authorID, Reason: reason,
			})
		},
	}
	cmd.Flags().StringVar(&handle, "handle", "", "Handle of a relay record from 'mtix sync quarantine list'")
	cmd.Flags().BoolVar(&accept, "accept", false, "Keep the record unapplied and record the review")
	cmd.Flags().BoolVar(&recreate, "recreate", false, "Create the skipped task again under a new id (create_node records only)")
	cmd.Flags().StringVar(&reason, "reason", "", "Required explanation, recorded in the receipt")
	for _, name := range []string{"handle", "reason"} {
		if err := cmd.MarkFlagRequired(name); err != nil {
			// This constructor just declared the flag; failure is a programming invariant.
			panic(fmt.Errorf("mark %s required: %w", name, err))
		}
	}
	return cmd
}

// relayResolveMode maps the two exclusive flags to a service mode.
func relayResolveMode(accept, recreate bool) (string, error) {
	switch {
	case accept && recreate:
		return "", fmt.Errorf("mtix sync quarantine resolve: --accept and --recreate are exclusive")
	case accept:
		return service.RelayResolveAccept, nil
	case recreate:
		return service.RelayResolveRecreate, nil
	}
	return "", fmt.Errorf("mtix sync quarantine resolve: choose --accept or --recreate")
}

// runSyncQuarantineResolve settles one relay row and prints the receipt.
func runSyncQuarantineResolve(ctx context.Context, w io.Writer, req service.RelayResolveRequest) error {
	if app.mtixDir == "" || app.store == nil || app.nodeSvc == nil {
		return fmt.Errorf("mtix sync quarantine resolve: local project store not initialized")
	}
	clock := func() time.Time { return time.Now().UTC() }
	res, err := service.NewRelayQuarantineResolver(app.store, app.nodeSvc, clock).Resolve(ctx, req)
	if err != nil {
		return fmt.Errorf("mtix sync quarantine resolve: %w", err)
	}
	if app.jsonOutput {
		return json.NewEncoder(w).Encode(res)
	}
	return printRelayResolveReceipt(w, res)
}

func printRelayResolveReceipt(w io.Writer, res *service.RelayResolveResult) error {
	var err error
	if res.RecreatedAs != "" {
		_, err = fmt.Fprintf(w, "recreated relay record %s rs %d as %s; resolved by %s at %s; reason: %s\n"+
			"note: %s syncs to every peer, including the author peer, which still holds the original and so holds both copies; "+
			"on the author peer close one: mtix cancel <id> --reason \"duplicate of <other-id>\"\n",
			res.PeerID, res.RS, res.RecreatedAs, oneLine(res.Actor), res.ResolvedAt, oneLine(res.Reason), res.RecreatedAs)
	} else {
		_, err = fmt.Fprintf(w, "accepted relay record %s rs %d: its content stays stored and unapplied, only in this project's store, so keep it "+
			"(read it with 'mtix sync quarantine list --json'); resolved by %s at %s; reason: %s\n",
			res.PeerID, res.RS, oneLine(res.Actor), res.ResolvedAt, oneLine(res.Reason))
	}
	if err != nil {
		return fmt.Errorf("write relay resolve receipt: %w", err)
	}
	return nil
}
