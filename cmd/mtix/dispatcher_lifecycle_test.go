//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestHostService_FailedInstall_RemovesUnit(t *testing.T) {
	initRegistrationApp(t)
	fakeHostManagers(t, "permission denied")
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	require.Error(t, runHostServiceAction(cmd, "install", true, 5))
	spec, err := currentHostServiceSpec(5)
	require.NoError(t, err)
	if spec.Path != "" {
		require.NoFileExists(t, spec.Path)
	}
	state, err := operatorState()
	require.NoError(t, err)
	require.False(t, hostDispatcherInstalled(state))
}

func TestHostService_UninstallAbsent_RemovesUnitAndRecord(t *testing.T) {
	initRegistrationApp(t)
	fakeHostManagers(t, "No such file or directory")
	spec, err := currentHostServiceSpec(5)
	require.NoError(t, err)
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	require.NoError(t, prepareHostInstall(cmd, spec))
	require.NoError(t, finishHostServiceAction("install", spec))
	require.NoError(t, runHostServiceAction(cmd, "uninstall", false, 5))
	if spec.Path != "" {
		require.NoFileExists(t, spec.Path)
	}
	state, err := operatorState()
	require.NoError(t, err)
	require.False(t, hostDispatcherInstalled(state))
	require.NoError(t, runHostServiceAction(cmd, "uninstall", false, 5))
}

func fakeHostManagers(t *testing.T, detail string) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native Unix service fixtures")
	}
	bin := t.TempDir()
	for _, name := range []string{"systemctl", "launchctl"} {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\ncase \"$*\" in *daemon-reload*) exit 0;; esac\necho '"+detail+"' >&2\nexit 1\n"), 0700))
	}
	t.Setenv("PATH", bin)
}

func TestPullHostCheckout_StripsAllExportedSyncCredentials(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"MTIX_SYNC_DSN", "MTIX_SYNC_PASSWORD", "MTIX_SYNC_SSLROOTCERT", "MTIX_SYNC_FUTURE_CREDENTIAL"} {
		t.Setenv(name, "inherited-secret")
	}
	exe := filepath.Join(root, "capture")
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/sh\nprintf 'dsn=%s\\npassword=%s\\nrootcert=%s\\nfuture=%s\\n' \"$MTIX_SYNC_DSN\" \"$MTIX_SYNC_PASSWORD\" \"$MTIX_SYNC_SSLROOTCERT\" \"$MTIX_SYNC_FUTURE_CREDENTIAL\" > child-env\n"), 0700))
	require.NoError(t, pullHostCheckout(context.Background(), exe, root, "checkout-secret"))
	output, err := os.ReadFile(filepath.Join(root, "child-env"))
	require.NoError(t, err)
	require.NotContains(t, string(output), "inherited-secret")
	require.Contains(t, strings.Split(string(output), "\n"), "dsn=checkout-secret")
}

func TestHostService_UninstallUnexpectedError_StillRemovesUnit(t *testing.T) {
	initRegistrationApp(t)
	fakeHostManagers(t, "permission denied")
	spec, err := currentHostServiceSpec(5)
	require.NoError(t, err)
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	require.NoError(t, prepareHostInstall(cmd, spec))
	require.NoError(t, finishHostServiceAction("install", spec))
	require.ErrorContains(t, runHostServiceAction(cmd, "uninstall", false, 5), "permission denied")
	if spec.Path != "" {
		require.NoFileExists(t, spec.Path)
	}
	state, err := operatorState()
	require.NoError(t, err)
	require.False(t, hostDispatcherInstalled(state))
}

func TestHostService_AllPlatforms_IdempotentLifecycle(t *testing.T) {
	for _, platform := range []string{"darwin", "linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			initRegistrationApp(t)
			spec, err := buildHostServiceSpec(platform, "/opt/bin/mtix", t.TempDir(), 5)
			require.NoError(t, err)
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			cmd.SetOut(io.Discard)
			var calls [][]string
			run := func(_ context.Context, argv []string) ([]byte, error) { calls = append(calls, argv); return nil, nil }
			for _, verb := range []string{"install", "install", "stop", "stop", "start", "start"} {
				require.NoError(t, applyHostServiceAction(cmd, spec, verb, run))
			}
			state, err := operatorState()
			require.NoError(t, err)
			require.True(t, hostDispatcherInstalled(state))
			if spec.Path != "" {
				require.FileExists(t, spec.Path)
			}
			for range 2 {
				require.NoError(t, applyHostServiceAction(cmd, spec, "uninstall", run))
			}
			if spec.Path != "" {
				require.NoFileExists(t, spec.Path)
			}
			require.False(t, hostDispatcherInstalled(state))
			require.NotEmpty(t, calls)
		})
	}
}

func TestHostService_AllPlatforms_FailedRegistrationRollsBack(t *testing.T) {
	for _, platform := range []string{"darwin", "linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			initRegistrationApp(t)
			spec, err := buildHostServiceSpec(platform, "/opt/bin/mtix", t.TempDir(), 5)
			require.NoError(t, err)
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			cmd.SetOut(io.Discard)
			failure := errors.New("registration refused")
			run := func(_ context.Context, argv []string) ([]byte, error) {
				args := strings.Join(argv, " ")
				if strings.Contains(args, "bootstrap") || strings.Contains(args, "enable") || strings.Contains(args, "/Create") {
					return nil, failure
				}
				return nil, nil
			}
			require.ErrorIs(t, applyHostServiceAction(cmd, spec, "install", run), failure)
			if spec.Path != "" {
				require.NoFileExists(t, spec.Path)
			}
			state, err := operatorState()
			require.NoError(t, err)
			require.False(t, hostDispatcherInstalled(state))
		})
	}
}

func TestHostService_NativeMissingJob_OnlyRemovalIsIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		argv   []string
		output string
		absent bool
	}{
		{"launchd missing", []string{"launchctl", "bootout", "gui/1/dispatcher"}, "Boot-out failed: 3: No such process", true},
		{"launchd denied", []string{"launchctl", "bootout", "gui/1/dispatcher"}, "Operation not permitted", false},
		{"launchd bootstrap", []string{"launchctl", "bootstrap", "gui/1", "unit"}, "No such process", false},
		{"systemd missing", []string{"systemctl", "--user", "disable", "--now", "unit"}, "Unit file unit does not exist.", true},
		{"systemd denied", []string{"systemctl", "--user", "disable", "--now", "unit"}, "Access denied", false},
		{"windows missing", []string{"schtasks", "/Delete", "/F", "/TN", "dispatcher"}, "ERROR: The system cannot find the file specified.", true},
		{"windows stopped", []string{"schtasks", "/End", "/TN", "dispatcher"}, "ERROR: The task is not currently running.", true},
		{"windows denied", []string{"schtasks", "/Delete", "/F", "/TN", "dispatcher"}, "Access is denied.", false},
		{"windows create", []string{"schtasks", "/Create", "/TN", "dispatcher"}, "The system cannot find the file specified.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := errors.New("exit 1")
			err := runHostServiceCommands(t.Context(), io.Discard, [][]string{tc.argv}, func(context.Context, []string) ([]byte, error) { return []byte(tc.output), failure })
			if tc.absent {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, failure)
			}
		})
	}
}

func TestHostService_AllPlatforms_StateRecordFailureRollsBack(t *testing.T) {
	for _, platform := range []string{"darwin", "linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			initRegistrationApp(t)
			spec, err := buildHostServiceSpec(platform, "/opt/bin/mtix", t.TempDir(), 5)
			require.NoError(t, err)
			invalid := filepath.Join(t.TempDir(), "config-root-is-file")
			require.NoError(t, os.WriteFile(invalid, nil, 0600))
			t.Setenv("XDG_CONFIG_HOME", invalid)
			t.Setenv("APPDATA", invalid)
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			cmd.SetOut(io.Discard)
			var calls [][]string
			run := func(_ context.Context, argv []string) ([]byte, error) { calls = append(calls, argv); return nil, nil }
			require.Error(t, applyHostServiceAction(cmd, spec, "install", run))
			if spec.Path != "" {
				require.NoFileExists(t, spec.Path)
			}
			require.Len(t, calls, len(spec.Register)+len(spec.Unregister))
			require.Equal(t, spec.Unregister, calls[len(spec.Register):])
		})
	}
}

func TestPullHostCheckout_SyncEnvironmentCaseAliases_NotInherited(t *testing.T) {
	for _, name := range []string{"mtix_sync_password", "mTiX_sYnC_SSLRootCert"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(name, "inherited-case-alias-secret")
			exe := filepath.Join(root, "capture-alias")
			script := "#!/bin/sh\nprintf '%s\\n' \"$" + name + "\" > child-alias\n"
			require.NoError(t, os.WriteFile(exe, []byte(script), 0700))
			require.NoError(t, pullHostCheckout(t.Context(), exe, root, "checkout-secret"))
			output, err := os.ReadFile(filepath.Join(root, "child-alias"))
			require.NoError(t, err)
			require.Empty(t, strings.TrimSpace(string(output)), name)
		})
	}
}
