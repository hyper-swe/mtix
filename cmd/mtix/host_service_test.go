// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Host units must be per-user, project-independent, and carry no credentials.
package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildHostServiceSpec_PerOS(t *testing.T) {
	for _, tc := range []struct{ os, expected, supervisor string }{{"darwin", "<key>KeepAlive</key><true/>", "launchd"}, {"linux", "Restart=always", "systemd"}, {"windows", "ONLOGON", "task-scheduler"}} {
		t.Run(tc.os, func(t *testing.T) {
			first, err := buildHostServiceSpec(tc.os, "/opt/mtix/bin/mtix", "/home/operator", 5)
			require.NoError(t, err)
			second, err := buildHostServiceSpec(tc.os, "/other/bin/mtix", "/home/operator", 9)
			require.NoError(t, err)
			require.Equal(t, first.Label, second.Label)
			all := first.Content
			for _, argv := range first.Register {
				all += " " + strings.Join(argv, " ")
			}
			require.Contains(t, all, tc.expected)
			require.Contains(t, all, "dispatcher")
			require.Contains(t, all, "--experimental")
			require.Contains(t, all, tc.supervisor)
			require.NotContains(t, all, "MTIX_SYNC_DSN")
			require.NotContains(t, all, "-C")
		})
	}
	_, err := buildHostServiceSpec("unsupported", "/bin/mtix", "/home/operator", 5)
	require.Error(t, err)
}
func TestHostDispatcher_ExportedDSN_Ignored(t *testing.T) {
	initRegistrationApp(t)
	for _, name := range []string{"MTIX_SYNC_DSN", "MTIX_SYNC_PASSWORD", "MTIX_SYNC_SSLROOTCERT", "MTIX_SYNC_FUTURE_CREDENTIAL"} {
		t.Setenv(name, "host-secret-must-not-be-used")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	root := newRootCmd()
	root.SetArgs([]string{"dispatcher", "run", "--experimental"})
	err := root.ExecuteContext(ctx)
	require.ErrorIs(t, err, context.Canceled)
	for _, name := range []string{"MTIX_SYNC_DSN", "MTIX_SYNC_PASSWORD", "MTIX_SYNC_SSLROOTCERT", "MTIX_SYNC_FUTURE_CREDENTIAL"} {
		require.Empty(t, os.Getenv(name), name)
	}
}
func TestHostDispatcher_DefaultOff_RefusesBeforeOpeningState(t *testing.T) {
	root := newRootCmd()
	for _, verb := range []string{"run", "install"} {
		t.Run(verb, func(t *testing.T) {
			cmd, _, err := root.Find([]string{"dispatcher", verb})
			require.NoError(t, err)
			require.ErrorContains(t, cmd.RunE(cmd, nil), "experimental")
		})
	}
}

func TestBuildHostServiceSpec_WindowsPath_PreservesBackslashes(t *testing.T) {
	exe := `C:\Program Files\MTIX\mtix.exe`
	spec, err := buildHostServiceSpec("windows", exe, `C:\Users\Operator`, 5)
	require.NoError(t, err)
	argv := spec.Register[0]
	require.Contains(t, argv[len(argv)-1], `"`+exe+`" dispatcher run`)
}

func TestHostLaunchd_StopUnloads_StartReloads(t *testing.T) {
	spec, err := buildHostServiceSpec("darwin", "/opt/bin/mtix", "/home/operator", 5)
	require.NoError(t, err)
	require.Equal(t, "bootout", spec.Stop[0][1])
	require.Equal(t, "bootstrap", spec.Start[len(spec.Start)-1][1])
	require.Equal(t, spec.Path, spec.Start[len(spec.Start)-1][3])
}

func TestHostDispatcher_SyncEnvironmentCaseAliases_Ignored(t *testing.T) {
	for _, name := range []string{"mtix_sync_password", "mTiX_sYnC_SSLRootCert"} {
		t.Run(name, func(t *testing.T) {
			initRegistrationApp(t)
			t.Setenv(name, "host-case-alias-secret")
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			root := newRootCmd()
			root.SetArgs([]string{"dispatcher", "run", "--experimental"})
			require.ErrorIs(t, root.ExecuteContext(ctx), context.Canceled)
			require.Empty(t, os.Getenv(name), name)
		})
	}
}
