// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// wireWakeScanner gives the CLI and daemon hook dispatcher the same gated wake scan the host
// dispatcher runs, so every mutation command and daemon tick also marks dead sessions stale
// and expires registrations unseen for 24 hours (ADR-007 section 2.7, FR-20, MTIX-112.17.37).
// It starts no timer and contacts no hub. A host with no registered identity gets no scanner,
// and a failure to build one never fails the command.
func wireWakeScanner(mtixDir string) {
	if app.hooksDisp == nil || app.store == nil {
		return
	}
	scanner, err := newTriggerWakeScanner(mtixDir)
	if err != nil {
		app.logger.Debug("wake scanner not wired", "error", err)
		return
	}
	if scanner == nil {
		return
	}
	app.wakeScanner = scanner
	app.hooksDisp.WithWakeScanner(scanner)
}

// newTriggerWakeScanner returns nil, nil when the host has no identity yet: nothing can be
// registered on such a host, and reading the identity creates no operator state.
func newTriggerWakeScanner(mtixDir string) (*service.WakeScanner, error) {
	host, err := existingRegistrationDoctorHost()
	if err != nil || host.HostID == "" {
		return nil, err
	}
	env, err := oplocal.FromProcess()
	if err != nil {
		return nil, fmt.Errorf("operator state: %w", err)
	}
	board, err := service.NewHostBoard(service.HostBoardOptions{
		Store: app.store, State: oplocal.New(env), Path: filepath.Dir(mtixDir),
		Host: host, PID: int64(os.Getpid()), Clock: utcNow, Logger: app.logger,
	})
	if err != nil {
		return nil, err
	}
	return board.WakeScanner(), nil
}
