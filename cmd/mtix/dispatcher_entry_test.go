// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the experimental host entry point and registration routing without network access.
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestHostDispatcher_EntryPoint_ExperimentalAndNoEnrollmentCommand(t *testing.T) {
	root := newRootCmd()
	dispatcher, _, err := root.Find([]string{"dispatcher"})
	require.NoError(t, err)
	for _, child := range dispatcher.Commands() {
		require.NotEqual(t, "enroll", child.Name())
		require.NotEqual(t, "unenroll", child.Name())
	}
	for _, verb := range []string{"run", "install", "start", "stop", "status", "uninstall"} {
		t.Run(verb, func(t *testing.T) {
			cmd, rest, err := root.Find([]string{"dispatcher", verb})
			require.NoError(t, err)
			require.Empty(t, rest)
			require.Equal(t, "mtix dispatcher "+verb, cmd.CommandPath())
			require.True(t, skipsStoreInit(cmd))
			if verb == "run" || verb == "install" {
				f := cmd.Flags().Lookup("experimental")
				require.NotNil(t, f)
				require.Equal(t, "false", f.DefValue)
			}
		})
	}
}

func TestHostDispatcher_RegistrationAutoListsCheckout_NoGrant(t *testing.T) {
	initRegistrationApp(t)
	id, err := service.CreateBoardID(app.mtixDir)
	require.NoError(t, err)
	require.NoError(t, runAgentRegister("test-worker", sessionOptions{}))
	state, err := operatorState()
	require.NoError(t, err)
	boards, err := state.ListedBoards(t.Context())
	require.NoError(t, err)
	require.Len(t, boards, 1)
	require.Equal(t, id, boards[0].Key)
	root, err := filepath.EvalSymlinks(filepath.Dir(app.mtixDir))
	require.NoError(t, err)
	require.Equal(t, root, boards[0].Path)
	grants, reports, err := state.BoardGrants()
	require.NoError(t, err)
	require.Empty(t, reports)
	require.Empty(t, grants)
}

func TestHostDispatcher_RegisteredCheckout_NoDialNoHook(t *testing.T) {
	initRegistrationApp(t)
	_, err := service.CreateBoardID(app.mtixDir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "secrets"), []byte("crafted-checkout-secret"), 0600))
	require.NoError(t, runAgentRegister("registered-worker", sessionOptions{}))
	state, err := operatorState()
	require.NoError(t, err)
	host, uid, err := registrationHostIdentity()
	require.NoError(t, err)
	dials, hooks := 0, 0
	dispatcher, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: state, OpenBoard: func(_ context.Context, row oplocal.ListedBoard) (*service.HostBoard, error) {
		return service.NewHostBoard(service.HostBoardOptions{Store: app.store, State: state, Path: row.Path, Host: service.RegistrationHost{HostID: host, UID: uid}, PID: int64(os.Getpid()), Clock: utcNow, Contact: service.NewContactScheduler(utcNow, func() (service.ContactInputs, error) {
			return service.ContactInputs{Mode: service.HubContactMode{Kind: service.HubContactInterval, Interval: 1}}, nil
		}), Pull: func(context.Context, string) error { dials++; return nil }, Hooks: func(context.Context) { hooks++ }})
	}})
	require.NoError(t, err)
	require.NoError(t, dispatcher.Pass(t.Context()))
	require.Zero(t, dials)
	require.Zero(t, hooks)
	registry, err := state.DispatchBoards(t.Context())
	require.NoError(t, err)
	require.Empty(t, registry.Enrolled)
}
