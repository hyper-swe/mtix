// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

func pushHandoffReleaseHolder(t *testing.T) {
	t.Helper()
	parked := false
	observer := func() error {
		if !parked {
			parked = true
			pushHandoffEmit(t, "last-held", "")
			pushHandoffRead(t)
		}
		return nil
	}
	ctx := context.WithValue(context.Background(), pushBeforeReleaseKey{}, observer)
	require.NoError(t, runSyncPush(ctx, io.Discard, os.Stderr, nil, transport.Options{InsecureTLS: true}, false))
	pushHandoffEmit(t, "done", "")
}

// This parks AFTER the native pass returns, at its last held-lock point. An
// inside-lock recheck mutant has already read empty before this readiness frame.
func TestRunSyncPush_ProcessReleaseBoundaryHandoff(t *testing.T) {
	pool := openCmdHub(t)
	dir := t.TempDir()
	store := newDaemonTestStore(t, dir)
	holder := startPushHandoffChild(t, dir, "release-holder")
	holder.frame(t, "last-held")
	contender := startPushHandoffChild(t, dir, "contender")
	contender.frame(t, "ready")
	contender.release(t)
	committed := contender.frame(t, "skipped")
	require.NotEmpty(t, committed.EventID)
	contender.join(t) // Actual native commit and nil contention precede Release.
	holder.release(t)
	holder.frame(t, "done")
	holder.join(t)
	handoffNativeDelivery(t, store, pool, committed.EventID, handoffDeliveryExpected{state: "pushed", hubCount: 1})
}
