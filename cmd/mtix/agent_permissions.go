// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/spf13/cobra"
)

func newAgentPermissionCmd(action string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: action + " <agent-id>", Short: action + " this host's wake permission for an agent",
		Args: cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runAgentPermission(args[0], action, yes)
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Record a non-interactive confirmation, including in a harness session")
	return cmd
}

func runAgentPermission(agent, action string, yes bool) error {
	if action == "allow" && !yes {
		if err := oplocal.RequireOperatorTTY(os.Stdin); err != nil {
			return err
		}
	}
	svc, board, err := configuredPermissions()
	if err != nil {
		return err
	}
	mode := "interactive"
	if yes || (action == "deny" && !oplocal.OperatorTerminal(os.Stdin)) {
		mode = "yes"
	}
	batch := oplocal.PermissionBatch{Mode: mode, Source: "mtix agent " + action,
		Markers: oplocal.HarnessSessionMarkers(os.Environ()),
		Changes: []oplocal.PermissionChange{{AgentID: agent, Action: action}}}
	if validationErr := svc.ValidateChanges(context.Background(), batch.Changes); validationErr != nil {
		return validationErr
	}
	if board.ID == "" {
		board.ID, err = service.CreateBoardID(app.mtixDir)
		if err != nil {
			return err
		}
	}
	if err := svc.ApplyBatch(context.Background(), board, batch); err != nil {
		return err
	}
	fmt.Printf("Wake permission %s recorded for %s on this host.\n", action, agent)
	return nil
}

func configuredPermissions() (*service.WakePermissions, service.PermissionBoard, error) {
	if app.store == nil || app.mtixDir == "" {
		return nil, service.PermissionBoard{}, fmt.Errorf("not in an mtix project")
	}
	env, err := oplocal.FromProcess()
	if err != nil {
		return nil, service.PermissionBoard{}, err
	}
	state := oplocal.New(env)
	host, err := state.HostID()
	if err != nil {
		return nil, service.PermissionBoard{}, err
	}
	uid, err := currentUserID()
	if err != nil {
		return nil, service.PermissionBoard{}, err
	}
	board, err := permissionBoard(state)
	if err != nil {
		return nil, board, err
	}
	svc := service.NewWakePermissions(service.PermissionDeps{
		Store: app.store, State: state, HostID: host, UID: uid, Clock: time.Now,
	})
	return svc, board, nil
}

func permissionBoard(state *oplocal.State) (service.PermissionBoard, error) {
	board := service.PermissionBoard{Checkout: filepath.Clean(filepath.Dir(app.mtixDir)), Evidence: lenientHubEvidence()}
	ids, err := service.ReadBoardIDs(app.mtixDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return board, err
	}
	if len(ids) > 1 {
		return board, fmt.Errorf("multiple board identities require reconciliation: %w", model.ErrConflict)
	}
	if len(ids) == 1 {
		board.ID = ids[0]
	}
	observation, reports, err := state.BoardObservation(board.Checkout)
	if err != nil || len(reports) != 0 {
		return board, fmt.Errorf("board observation unavailable: %w", model.ErrOperatorStateUnreadable)
	}
	board.Hub = observation.Hub
	return board, nil
}
