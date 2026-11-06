// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
)

// newDaemonScheduler builds the daemon's ContactScheduler (MTIX-112.20.1).
// Without a hub there is nothing to schedule: it returns nil and the pass
// loop keeps its fixed local cadence. With a hub the scheduler's inputs come
// from the board config and operator-local state, read fresh every pass. A
// grandfathered board keeps contacting at the daemon's own --interval.
//
// The returned ReplyProcessor is the same reply accounting the host dispatcher
// runs (MTIX-112.20.3, newReplyProcessor): the loop ticks it before each
// scheduling decision and scans after each pull, so a reply closes a sender
// window and a reply_by event for a local agent starts a hold here too.
func newDaemonScheduler(stdout, stderr io.Writer, hub bool, intervalSec int) (*service.ContactScheduler, func(daemonPullOutcome), *service.ReplyProcessor, error) {
	if !hub {
		return nil, nil, nil, nil
	}
	src, err := newHubContactSource(app.mtixDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("mtix daemon: hub contact state: %w", err)
	}
	src.grandfatherInterval = time.Duration(intervalSec) * time.Second
	if mode, grandfathered, modeErr := daemonHubContactMode(src); modeErr == nil {
		announceHubContact(stdout, service.NewCostWarner(), mode, grandfathered)
	}
	if _, snapErr := src.snapshot(); snapErr != nil {
		fmt.Fprintf(stderr, "mtix daemon: hub contact inputs unreadable, no hub contact until fixed (run: mtix doctor): %s\n", snapErr)
	}
	onContact := func(outcome daemonPullOutcome) {
		if recErr := src.recordContact(src.now(), "poll", outcome); recErr != nil {
			fmt.Fprintf(stderr, "mtix daemon: %s\n", recErr)
		}
	}
	return service.NewContactScheduler(src.now, src.snapshot), onContact, daemonReplies(stderr, src), nil
}

// daemonReplies builds the daemon's reply processor on the shared wiring.
// Without a host identity no registration can match, so only sender windows
// work; the warning names the fix.
func daemonReplies(stderr io.Writer, src *hubContactSource) *service.ReplyProcessor {
	var host service.RegistrationHost
	hostID, uid, err := registrationHostIdentity()
	if err != nil {
		fmt.Fprintf(stderr, "mtix daemon: host identity unreadable, recipient reply holds are off (run: mtix doctor): %s\n", err)
	} else {
		host = service.RegistrationHost{HostID: hostID, UID: uid}
	}
	return newReplyProcessor(app.store, src.state, src, host)
}

func daemonHubContactMode(src *hubContactSource) (service.HubContactMode, bool, error) {
	cfg, err := service.NewConfigService(src.configPath())
	if err != nil {
		return service.HubContactMode{}, false, fmt.Errorf("load config: %w", err)
	}
	return cfg.HubContact(src.grandfatherInterval)
}

// announceHubContact tells the operator, once per daemon start, how the hub
// will be contacted: the grandfather notice with the exact switch command,
// and the interval-mode cost warning.
func announceHubContact(w io.Writer, warner *service.CostWarner, mode service.HubContactMode, grandfathered bool) {
	if grandfathered {
		fmt.Fprintf(w, "mtix daemon: hub contact is grandfathered at %s (this board predates sync.hub_contact); "+
			"to contact the hub only after activity, run: mtix config set sync.hub_contact activity\n", mode)
	}
	if msg := warner.Warn(mode); msg != "" {
		fmt.Fprintf(w, "mtix daemon: %s\n", msg)
	}
}
