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
func newDaemonScheduler(stdout, stderr io.Writer, hub bool, intervalSec int) (*service.ContactScheduler, func(), error) {
	if !hub {
		return nil, nil, nil
	}
	src, err := newHubContactSource(app.mtixDir)
	if err != nil {
		return nil, nil, fmt.Errorf("mtix daemon: hub contact state: %w", err)
	}
	src.grandfatherInterval = time.Duration(intervalSec) * time.Second
	if mode, grandfathered, modeErr := daemonHubContactMode(src); modeErr == nil {
		announceHubContact(stdout, service.NewCostWarner(), mode, grandfathered)
	}
	if _, snapErr := src.snapshot(); snapErr != nil {
		fmt.Fprintf(stderr, "mtix daemon: hub contact inputs unreadable, no hub contact until fixed (run: mtix doctor): %s\n", snapErr)
	}
	onContact := func() {
		if recErr := src.recordContact(src.now(), "poll"); recErr != nil {
			fmt.Fprintf(stderr, "mtix daemon: %s\n", recErr)
		}
	}
	return service.NewContactScheduler(src.now, src.snapshot), onContact, nil
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
