// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// dispatcherRestartDeps are the seams of the CLI restart trigger.
type dispatcherRestartDeps struct {
	statuses    []service.DispatcherStatus
	manager     attach.ServiceManager
	state       *oplocal.State
	marked      bool
	cliProtocol int
	clock       func() time.Time
}

// restartOlderDispatchers applies ADR-007 §6.4 when this CLI meets a
// dispatcher older than itself: at most one restart per hour, never under a
// harness session marker. Anything short of a real restart prints one line
// naming why and raises a wake.version-skew alert; doctor skew.dispatchers
// keeps showing the dispatcher until it runs the current mtix.
func restartOlderDispatchers(ctx context.Context, w io.Writer, deps dispatcherRestartDeps) {
	restarter := attach.NewRestarter(deps.manager, deps.state, deps.clock)
	for _, d := range deps.statuses {
		outcome, err := restarter.MaybeRestart(ctx, attach.RestartRequest{
			CLIProtocol: deps.cliProtocol, DispatcherPID: d.PID, DispatcherProtocol: d.Protocol, HarnessMarked: deps.marked,
		})
		line := restartLine(d, outcome, err)
		if line == "" {
			continue
		}
		_, _ = fmt.Fprintln(w, line) //nolint:errcheck // stderr notice; the alert below is the durable record
		if outcome == attach.OutcomeRestarted {
			continue
		}
		if _, alertErr := service.NewAlerts(deps.state, deps.clock).Raise(ctx, service.AlertRequest{
			Agent: "dispatcher", Class: "wake.version-skew", Detail: line,
			Fix: fmt.Sprintf("stop and start the dispatcher process PID %d so it runs the current mtix", d.PID),
		}); alertErr != nil {
			_, _ = fmt.Fprintf(w, "mtix: could not record an operator alert: %v\n", alertErr) //nolint:errcheck // last-resort channel
		}
	}
}

func restartLine(d service.DispatcherStatus, outcome attach.Outcome, err error) string {
	who := fmt.Sprintf("dispatcher PID %d (attach protocol %d)", d.PID, d.Protocol)
	switch outcome {
	case attach.OutcomeCurrent:
		return ""
	case attach.OutcomeRestarted:
		return fmt.Sprintf("mtix: restarted %s so it runs this mtix", who)
	case attach.OutcomeRateLimited:
		return fmt.Sprintf("mtix: %s still runs an older attach protocol after the restart mtix made less than an hour ago; it is not restarted again yet; stop and start PID %d yourself", who, d.PID)
	case attach.OutcomeHarnessMarker:
		return fmt.Sprintf("mtix: %s is older than this mtix and was not restarted, because this command runs inside a harness session; see mtix doctor check skew.dispatchers", who)
	case attach.OutcomeNoService:
		return fmt.Sprintf("mtix: %s is older than this mtix and was not restarted: no dispatcher service is installed; stop and start PID %d yourself (mtix doctor check skew.dispatchers)", who, d.PID)
	default:
		return fmt.Sprintf("mtix: %s is older than this mtix and was not restarted (%s: %v); see mtix doctor check skew.dispatchers", who, outcome, err)
	}
}

// productionDispatcherRestart reads this host's dispatcher heartbeats after
// a session registers and applies the restart rules. This build installs no
// dispatcher service, so its service manager is attach.NoServiceManager.
func productionDispatcherRestart(ctx context.Context, w io.Writer) {
	if app.store == nil {
		return
	}
	host, uid, err := registrationHostIdentity()
	if err != nil {
		return // registration already reported the identity gap
	}
	statuses, err := service.NewDispatcherSkew(app.store, host, uid, attach.Protocol).Statuses(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(w, "mtix: could not read dispatcher heartbeats: %v\n", err) //nolint:errcheck // stderr notice
		return
	}
	st, err := operatorState()
	if err != nil {
		_, _ = fmt.Fprintf(w, "mtix: could not open operator state: %v\n", err) //nolint:errcheck // stderr notice
		return
	}
	restartOlderDispatchers(ctx, w, dispatcherRestartDeps{
		statuses: statuses, manager: attach.NoServiceManager{}, state: st, marked: hooks.SessionMarked(),
		cliProtocol: attach.Protocol, clock: func() time.Time { return time.Now().UTC() },
	})
}
