// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
)

func TestHostDispatcher_ServiceRestart_UsesRestartVerb(t *testing.T) {
	for _, tc := range []struct{ platform, command string }{{"linux", "systemctl"}, {"darwin", "launchctl"}, {"windows", "schtasks"}} {
		t.Run(tc.platform, func(t *testing.T) {
			spec, err := buildHostServiceSpec(tc.platform, "/bin/mtix", "/home/operator", 5)
			require.NoError(t, err)
			var called [][]string
			require.NoError(t, restartHostService(context.Background(), tc.platform, spec, func(_ context.Context, argv [][]string) error { called = argv; return nil }))
			require.NotEmpty(t, called)
			require.Equal(t, tc.command, called[0][0])
			if tc.platform == "linux" {
				require.Contains(t, called[0], "restart")
			}
			if tc.platform == "darwin" {
				require.Contains(t, called[0], "kickstart")
				require.Contains(t, called[0], "-k")
			}
		})
	}
}
func TestHostDispatcher_ServiceRestart_NotInstalled_IsHonest(t *testing.T) {
	initRegistrationApp(t)
	err := (hostServiceManager{}).Restart(t.Context())
	require.ErrorIs(t, err, attach.ErrNoDispatcherService)
}

func TestHostDispatcher_ChangedTarget_SelfRestartOnlyUnderReliableSupervisor(t *testing.T) {
	for _, tc := range []struct {
		name       string
		supervisor attach.Supervisor
		exit       bool
	}{
		{"foreground", attach.SupervisorForeground, false}, {"launchd", attach.SupervisorKeepAlive, true}, {"systemd", attach.SupervisorRestartAlways, true}, {"task-scheduler", attach.SupervisorBestEffort, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initRegistrationApp(t)
			state, err := operatorState()
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "mtix")
			require.NoError(t, os.WriteFile(path, []byte("original"), 0700))
			physical, err := attach.ValidateShimTarget(path, os.Getuid())
			require.NoError(t, err)
			initial, err := os.Stat(physical)
			require.NoError(t, err)
			require.False(t, checkHostTarget(t.Context(), state, tc.supervisor, hostTargetSnapshot{target: path, physical: physical, initial: initial}))
			require.NoError(t, os.WriteFile(path, []byte("updated binary contents"), 0700))
			require.Equal(t, tc.exit, checkHostTarget(t.Context(), state, tc.supervisor, hostTargetSnapshot{target: path, physical: physical, initial: initial}))
			require.NoError(t, os.Remove(path))
			require.False(t, checkHostTarget(t.Context(), state, tc.supervisor, hostTargetSnapshot{target: path, physical: physical, initial: initial}), "an invalid target must never trigger restart")
		})
	}
}
