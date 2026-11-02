// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// piWaitPoll is how often a wait child re-reads its local board. It touches only the
// local database, never a hub, so it cannot wake a scale-to-zero database.
const piWaitPoll = 2 * time.Second

// piWaitLease is how long a claim holds the event before another claimer may take it.
const piWaitLease = 5 * time.Minute

// productionAttachAdapters returns the harness adapters this build ships. The pi adapter
// serves the wait verb for glue that was installed by hand: no default path installs the
// pi extension (MTIX-112.19.7, experimental).
func productionAttachAdapters() map[string]attach.Adapter {
	adapters := map[string]attach.Adapter{}
	pi, err := service.NewPiWait(service.PiWaitOptions{
		Open: openPiWaitBoard,
		Host: func() (string, string, error) {
			host, err := existingRegistrationDoctorHost()
			if err != nil {
				return "", "", err
			}
			if host.HostID == "" {
				return "", "", fmt.Errorf("this host has no mtix identity yet: nothing is registered here")
			}
			return host.HostID, host.UID, nil
		},
		Lease: piWaitLease,
		Wait:  piParentWatch(os.Getppid, piWaitPoll),
	})
	if err == nil {
		adapters[attach.HarnessPi] = pi
	}
	return adapters
}

// openPiWaitBoard opens the board of the working directory without creating anything: a
// directory with no initialized board is inert and the wait fails with one line.
func openPiWaitBoard(context.Context) (service.PiWaitStore, func() error, error) {
	mtixDir, err := findMtixDir()
	if err != nil {
		return nil, nil, fmt.Errorf("no mtix board here: %w", err)
	}
	dbDir := filepath.Join(mtixDir, "data")
	if info, statErr := os.Stat(dbDir); statErr != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("the board at %s has no data directory", mtixDir)
	}
	st, err := sqlite.New(dbDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return nil, nil, fmt.Errorf("open board: %w", err)
	}
	return st, st.Close, nil
}

// piParentWatch returns the wait between polls. It ends the wait with an error as soon as
// the parent process changes (pi died, so the child was re-parented), so an orphaned wait
// child never polls forever.
func piParentWatch(getppid func() int, poll time.Duration) func(context.Context) error {
	parent := getppid()
	return func(ctx context.Context) error {
		t := time.NewTimer(poll)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait cancelled: %w", ctx.Err())
		case <-t.C:
		}
		if now := getppid(); now != parent {
			return fmt.Errorf("the parent process changed from %d to %d: pi is gone", parent, now)
		}
		return nil
	}
}
