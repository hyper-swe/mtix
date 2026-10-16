// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Supervisor is how the host dispatcher's process is kept running.
type Supervisor int

// Supervisors (ADR-007 §6.4).
const (
	// SupervisorForeground is a dispatcher run by hand; nothing restarts it.
	SupervisorForeground Supervisor = iota
	// SupervisorKeepAlive is a launchd agent with KeepAlive.
	SupervisorKeepAlive
	// SupervisorRestartAlways is a systemd user unit with Restart=always.
	SupervisorRestartAlways
	// SupervisorBestEffort is a scheduler with best-effort restart (Task Scheduler).
	SupervisorBestEffort
)

// RestartsOnExit reports whether the supervisor reliably restarts an exited
// dispatcher: only launchd KeepAlive and systemd Restart=always do.
func (s Supervisor) RestartsOnExit() bool {
	return s == SupervisorKeepAlive || s == SupervisorRestartAlways
}

// SelfRestartInput is what the dispatcher knows after re-resolving the shim
// target at the end of a pass.
type SelfRestartInput struct {
	Supervisor    Supervisor
	TargetChanged bool
	TargetValid   bool
}

// SelfRestart is the dispatcher's decision: exit at an idle point, raise a
// skew alert, or both false to keep running quietly.
type SelfRestart struct {
	Exit   bool
	Alert  bool
	Reason string
}

// DecideSelfRestart applies ADR-007 §6.4: a dispatcher whose shim target
// changed and validates exits at an idle point only when its supervisor
// restarts it; otherwise it keeps running and raises a skew alert.
func DecideSelfRestart(in SelfRestartInput) SelfRestart {
	switch {
	case !in.TargetChanged:
		return SelfRestart{}
	case !in.TargetValid:
		return SelfRestart{Alert: true, Reason: "the new mtix target is refused by the shim rules; the dispatcher keeps running the old binary"}
	case in.Supervisor.RestartsOnExit():
		return SelfRestart{Exit: true}
	default:
		return SelfRestart{Alert: true, Reason: "mtix was upgraded but no supervisor restarts this dispatcher; restart it yourself"}
	}
}

// ErrNoDispatcherService means no host dispatcher service is installed, so
// there is nothing a service manager could restart.
var ErrNoDispatcherService = errors.New("no dispatcher service is installed")

// ServiceManager restarts the installed host dispatcher service.
type ServiceManager interface {
	Restart(ctx context.Context) error
}

// NoServiceManager is the service manager of a host with no dispatcher
// service installed: Restart always reports ErrNoDispatcherService.
type NoServiceManager struct{}

// Restart implements ServiceManager; it never reports success.
func (NoServiceManager) Restart(context.Context) error {
	return fmt.Errorf("restart host dispatcher: %w", ErrNoDispatcherService)
}

// Outcome is what a CLI did about an older dispatcher.
type Outcome string

// Restart outcomes; only OutcomeRestarted means a restart happened.
const (
	OutcomeCurrent         Outcome = "current"
	OutcomeHarnessMarker   Outcome = "held-harness-session"
	OutcomeRateLimited     Outcome = "rate-limited"
	OutcomeStateUnwritable Outcome = "operator-state-unwritable"
	OutcomeNoService       Outcome = "no-dispatcher-service"
	OutcomeUnreachable     Outcome = "service-manager-unreachable"
	OutcomeRestarted       Outcome = "restarted"
)

// restartRecordName holds the time of the last real CLI restart.
const restartRecordName = "dispatcher-restart"

// restartInterval is the minimum time between real CLI restarts.
const restartInterval = time.Hour

type restartRecord struct {
	LastRestart time.Time `json:"last_restart"`
}

// RestartRequest describes one dispatcher a CLI met.
type RestartRequest struct {
	CLIProtocol        int
	DispatcherPID      int64
	DispatcherProtocol int
	HarnessMarked      bool
}

// Restarter applies the CLI restart rules of ADR-007 §6.4.
type Restarter struct {
	manager ServiceManager
	store   RecordStore
	clock   func() time.Time
}

// NewRestarter wires a service manager, operator-local records and a clock.
func NewRestarter(manager ServiceManager, store RecordStore, clock func() time.Time) *Restarter {
	return &Restarter{manager: manager, store: store, clock: clock}
}

// MaybeRestart restarts an older dispatcher through the service manager at
// most once per hour, never under a harness session marker (a sandboxed agent
// CLI must not gain OS-service control) and never when operator state cannot
// be written. Only a real restart starts the hourly limit, so a host with no
// dispatcher service reports OutcomeNoService on every attempt. Only
// OutcomeRestarted means a restart happened.
func (r *Restarter) MaybeRestart(ctx context.Context, req RestartRequest) (Outcome, error) {
	if req.DispatcherProtocol >= req.CLIProtocol {
		return OutcomeCurrent, nil
	}
	if req.HarnessMarked {
		return OutcomeHarnessMarker, nil
	}
	var last restartRecord
	reports, err := r.store.ReadJSON(restartRecordName, &last)
	if err != nil {
		return OutcomeStateUnwritable, fmt.Errorf("read dispatcher restart record: %w", err)
	}
	if len(reports) > 0 {
		last = restartRecord{}
	}
	now := r.clock()
	if !last.LastRestart.IsZero() && now.Before(last.LastRestart.Add(restartInterval)) {
		return OutcomeRateLimited, nil
	}
	// Writing the unchanged record proves operator state is writable here;
	// a CLI that cannot write it (a sandbox) never touches the service manager.
	if writeErr := r.store.WriteJSON(restartRecordName, last); writeErr != nil {
		return OutcomeStateUnwritable, fmt.Errorf("check dispatcher restart record: %w", writeErr)
	}
	err = r.manager.Restart(ctx)
	switch {
	case errors.Is(err, ErrNoDispatcherService):
		return OutcomeNoService, nil
	case err != nil:
		return OutcomeUnreachable, fmt.Errorf("restart dispatcher pid %d: %w", req.DispatcherPID, err)
	}
	if writeErr := r.store.WriteJSON(restartRecordName, restartRecord{LastRestart: now}); writeErr != nil {
		return OutcomeRestarted, fmt.Errorf("record dispatcher restart: %w", writeErr)
	}
	return OutcomeRestarted, nil
}
