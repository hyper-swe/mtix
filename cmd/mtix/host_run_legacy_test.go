// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The production host board honors a live 0.5.x service (ADR-007 D11, MTIX-112.17.15).
package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestOpenHostBoard_LegacyServiceLive_NoPullThenPullWhenGone builds the board through the real
// openHostBoard, so removing the legacy probe from that wiring fails here. The control pass
// proves the fixture really pulls when no legacy service exists.
func TestOpenHostBoard_LegacyServiceLive_NoPullThenPullWhenGone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the pull child")
	}
	initRegistrationApp(t)
	require.NoError(t, runAgentRegister("legacy-worker", sessionOptions{}))
	t.Setenv("HOME", t.TempDir()) // no per-project unit exists under this home
	root, err := filepath.EvalSymlinks(filepath.Dir(app.mtixDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "secrets"), []byte("checkout-secret"), 0o600))
	state, err := operatorState()
	require.NoError(t, err)
	instance, err := app.store.StoreInstanceID(t.Context())
	require.NoError(t, err)
	hub := oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "observed"}
	entry := &oplocal.EnrolledBoard{Path: root, BoardID: instance, StoreInstanceID: instance, Hub: hub}
	require.NoError(t, state.SetEnrollment(t.Context(), entry, oplocal.EnrollmentAudit{Path: root, Action: "enroll", Mode: "yes", Source: "test", At: time.Now().UTC()}))

	marker := filepath.Join(t.TempDir(), "pulled")
	exe := filepath.Join(t.TempDir(), "fake-mtix")
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/sh\necho pulled >> '"+marker+"'\n"), 0o700))
	host := service.RegistrationHost{HostID: "legacy-host", UID: "legacy-uid", Version: "test", Protocol: attach.Protocol}
	dispatcher, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: state, OpenBoard: func(ctx context.Context, row oplocal.ListedBoard) (*service.HostBoard, error) {
		return openHostBoard(ctx, row, state, host, exe)
	}})
	require.NoError(t, err)

	require.NoError(t, dispatcher.Pass(t.Context()))
	require.FileExists(t, marker, "control: with no legacy service the enrolled board is pulled")
	require.NoError(t, os.Remove(marker))

	require.NoError(t, writeDaemonPID(app.mtixDir, startLegacyDaemonHelper(t, root)))
	require.NoError(t, dispatcher.Pass(t.Context()))
	require.NoFileExists(t, marker, "a live 0.5.x daemon keeps pull and hook duty")
}
