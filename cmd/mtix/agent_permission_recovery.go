// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/spf13/cobra"
)

func newAgentPermissionsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "permissions", Short: "Inspect and recover this host's wake permission batches"}
	cmd.AddCommand(newAgentPermissionRecoveryCmd())
	return cmd
}

func newAgentPermissionRecoveryCmd() *cobra.Command {
	var cancel string
	cmd := &cobra.Command{
		Use: "recover", Short: "List stuck batches and quarantine holds, or cancel an uncommitted batch with audit",
		Long: "List this host's pending wake permission batches and legacy quarantine holds. A quarantine hold is cleared only by a fresh operator allow in that board checkout; the file API cannot clear it. Use --cancel <batch-id> to cancel or quarantine its uncommitted allows, preserve accepted denies and committed state, and permanently refuse stale caller retries. Cancellation never grants permission.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := oplocal.FromProcess()
			if err != nil {
				return err
			}
			state := oplocal.New(env)
			if cancel != "" {
				if err := state.CancelPermissionBatch(cmd.Context(), cancel); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Permission batch cancelled with audit; accepted denies retained.")
				return nil
			}
			return listPendingPermissions(cmd, state)
		},
	}
	cmd.Flags().StringVar(&cancel, "cancel", "", "Cancel this pending caller batch id with durable audit; never grants permission")
	return cmd
}

func listPendingPermissions(cmd *cobra.Command, state *oplocal.State) error {
	batches, err := state.PendingPermissionBatches(cmd.Context())
	if err != nil {
		return err
	}
	holds, err := state.QuarantinedPermissionGrants(cmd.Context())
	if err != nil {
		return err
	}
	if app.jsonOutput {
		report := struct {
			Pending []oplocal.PermissionBatch `json:"pending_batches"`
			Holds   []oplocal.AgentGrant      `json:"quarantine_holds"`
		}{batches, holds}
		if encodingErr := json.NewEncoder(cmd.OutOrStdout()).Encode(report); encodingErr != nil {
			return fmt.Errorf("encode pending permissions: %w", encodingErr)
		}
		return nil
	}
	if len(batches) == 0 && len(holds) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No pending wake permission batches or quarantine holds.")
		return nil
	}
	for _, batch := range batches {
		id := batch.CallerID
		if id == "" {
			id = batch.ID
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s board=%s mode=%s source=%s; cancel: mtix agent permissions recover --cancel %s\n", id, batch.BoardID, batch.Mode, batch.Source, id)
	}
	for _, hold := range holds {
		fmt.Fprintf(cmd.OutOrStdout(), "%s board=%s quarantine hold from batch %s; clear on that board: mtix agent allow %s\n", hold.AgentID, hold.BoardID, hold.QuarantineBatchID, hold.AgentID)
	}
	return nil
}
