// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/service"
)

const (
	// doctorListenerRow is the row name; it sits in the wake.listener family
	// (MTIX-112.40.6) and is the eighth row of it once both land.
	doctorListenerRow = "wake.listener.no-hub-listener"
	fixStartListener  = "mtix daemon --listen"
)

// listenerDoctorRow reports a waiter lease with no listener behind it
// (MTIX-112.20.4): the waiter is parked and the hub or relay is never read
// for it. Local reads only; it never contacts the hub.
func listenerDoctorRow(src *hubContactSource, hub, relay bool, lockHeld func(string) (bool, error)) DoctorCheck {
	row := DoctorCheck{Name: doctorListenerRow, Pass: true}
	if !hub && !relay {
		row.Detail = "no hub or relay is configured, so no listener is needed"
		return row
	}
	cfg, err := service.NewConfigService(src.configPath())
	if err != nil {
		row.Pass, row.Detail, row.Fix = false, err.Error(), fixHubContact
		return row
	}
	if p := planListener(cfg, hub, relay, src.grandfatherInterval); !p.Join {
		row.Detail = "no listener is expected: " + p.Reason
		return row
	}
	leases, err := src.liveLeaseList()
	if err != nil {
		row.Pass, row.Detail, row.Fix = false, err.Error(), fixOperatorHome
		return row
	}
	if len(leases) == 0 {
		row.Detail = "no live waiter lease, so no listener is needed"
		return row
	}
	if !relay {
		if idle, reason := holdEnded(src); idle {
			// A listener started now would exit at once (the hold has ended), so
			// demanding one is a fix that cannot go green: report the idle state.
			row.Detail = "waiters are parked and the hub is idle-clamped, so no listener is expected: " + reason
			return row
		}
	}
	held, err := lockHeld(src.mtixDir)
	if err != nil {
		row.Pass, row.Detail, row.Fix = false, "cannot check for a running listener: "+err.Error(), fixOperatorHome
		return row
	}
	agents := make([]string, 0, len(leases))
	for _, l := range leases {
		agents = append(agents, fmt.Sprintf("%s (pid %d)", l.Agent, l.PID))
	}
	if held {
		row.Detail = "listener running for waiters: " + strings.Join(agents, ", ")
		return row
	}
	row.Pass = false
	row.Detail = "waiters are parked with no listener, so the hub and relay are not read for them: " + strings.Join(agents, ", ")
	row.Fix = fixStartListener
	return row
}

// holdEnded reports whether the listen hold is not armed now (idle clamp
// passed or no activity recorded) and says why. It applies the same rule
// the listener uses to exit (daemonListener.Done), so doctor never asks for
// a listener that would stop at once.
func holdEnded(src *hubContactSource) (bool, string) {
	in, err := src.snapshot()
	if err != nil {
		return false, "" // unreadable inputs are reported by the sync.listen row
	}
	if armed, _ := service.ListenHoldArmed(in, src.now()); armed {
		return false, ""
	}
	return true, "the listen hold has ended (idle clamp passed or no activity recorded); new activity re-arms it on the next wait"
}
