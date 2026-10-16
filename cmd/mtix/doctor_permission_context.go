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
)

type permissionDoctorContext struct {
	permissions *service.WakePermissions
	board       service.PermissionBoard
	err         error
	relevant    bool
}

func permissionDoctorFailure(err error) permissionDoctorContext {
	return permissionDoctorContext{err: err, relevant: true}
}

func readPermissionDoctorContext() permissionDoctorContext {
	board, err := permissionDoctorIdentity()
	if err != nil {
		return permissionDoctorFailure(err)
	}
	env, err := oplocal.FromProcess()
	if err != nil {
		return permissionDoctorFailure(err)
	}
	state := oplocal.New(env)
	host, err := state.ReadHostID()
	if err != nil {
		return permissionDoctorFailure(err)
	}
	if host == "" {
		present, presenceErr := state.PermissionStatePresent()
		if presenceErr != nil {
			return permissionDoctorFailure(presenceErr)
		}
		if present {
			return permissionDoctorFailure(fmt.Errorf("permission state has no host identity: %w", model.ErrOperatorStateUnreadable))
		}
		return permissionDoctorContext{board: board}
	}
	return permissionDoctorDependencies(state, host, board)
}

func permissionDoctorIdentity() (service.PermissionBoard, error) {
	board := service.PermissionBoard{Checkout: filepath.Clean(filepath.Dir(app.mtixDir)), Evidence: collectHubEvidence()}
	if app.store == nil || app.mtixDir == "" {
		return board, fmt.Errorf("permission diagnostics need an initialized project: %w", model.ErrInvalidInput)
	}
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
	return board, nil
}

func permissionDoctorDependencies(state *oplocal.State, host string, board service.PermissionBoard) permissionDoctorContext {
	uid, err := currentUserID()
	if err != nil {
		return permissionDoctorFailure(err)
	}
	observation, reports, err := state.BoardObservation(board.Checkout)
	if err != nil || len(reports) != 0 {
		return permissionDoctorFailure(fmt.Errorf("board observation unavailable: %w", errors.Join(err, model.ErrOperatorStateUnreadable)))
	}
	board.Hub = observation.Hub
	permissions := service.NewWakePermissions(service.PermissionDeps{Store: app.store, State: state, HostID: host, UID: uid, Clock: time.Now})
	rows, registrationErr := app.store.ListRegistrations(context.Background(), host, uid)
	confirmations, confirmationErr := state.Confirmations(context.Background())
	grants, grantErr := state.AgentGrants()
	err = errors.Join(registrationErr, confirmationErr, grantErr)
	return permissionDoctorContext{permissions: permissions, board: board, err: err,
		relevant: len(rows) != 0 || len(confirmations) != 0 || len(grants) != 0 || err != nil}
}

func permissionDoctorError(err error) service.DoctorResult {
	fix := "Restore readable protected operator state, then run mtix agent permissions recover"
	if errors.Is(err, model.ErrConflict) {
		fix = "Reconcile .mtix/board-id without discarding either identity, then run mtix doctor --json"
	}
	return service.DoctorResult{Status: service.DoctorFail, Detail: err.Error(), Fix: fix}
}
