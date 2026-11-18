// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func joinCmds(cmds [][]string) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = strings.Join(c, " ")
	}
	return out
}

func specFor(t *testing.T, goos string) *serviceSpec {
	t.Helper()
	spec, err := buildServiceSpec(goos, "/bin/mtix", "/work/proj", "/home/u", 5)
	require.NoError(t, err)
	return spec
}

// TestServiceSpec_Stop_HoldsAgainstRestartPolicyPerOS pins the generated stop command
// sequence per OS (MTIX-275): a stop must unload or disable the service so its restart
// policy (launchd KeepAlive, systemd enable, a logon task) cannot bring it back, and must
// never be a signal that KeepAlive answers by restarting.
func TestServiceSpec_Stop_HoldsAgainstRestartPolicyPerOS(t *testing.T) {
	tests := []struct {
		goos     string
		contains []string
		last     string
	}{
		{"darwin", []string{"launchctl disable gui/", "launchctl bootout gui/"}, "launchctl bootout gui/"},
		{"linux", []string{"systemctl --user disable --now mtix-daemon-"}, "systemctl --user disable --now mtix-daemon-"},
		{"windows", []string{"schtasks /End /TN mtix-daemon-", "/Change /TN mtix-daemon-", "/DISABLE"}, "/DISABLE"},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			stop := strings.Join(joinCmds(specFor(t, tt.goos).Stop), ";")
			for _, want := range tt.contains {
				assert.Contains(t, stop, want)
			}
			assert.NotContains(t, stop, "kill", "a signal is answered by KeepAlive with a restart")
			assert.Contains(t, joinCmds(specFor(t, tt.goos).Stop)[len(specFor(t, tt.goos).Stop)-1], tt.last)
		})
	}
}

// TestServiceSpec_Start_UndoesStopPerOS pins that start reverses everything stop did.
func TestServiceSpec_Start_UndoesStopPerOS(t *testing.T) {
	tests := []struct {
		goos     string
		contains []string
		last     string
	}{
		{"darwin", []string{"launchctl enable gui/", "launchctl bootstrap gui/", "launchctl kickstart -k gui/"}, "launchctl kickstart -k"},
		{"linux", []string{"systemctl --user enable --now mtix-daemon-"}, "enable --now"},
		{"windows", []string{"/Change /TN mtix-daemon-", "/ENABLE", "schtasks /Run /TN"}, "schtasks /Run /TN"},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			spec := specFor(t, tt.goos)
			start := strings.Join(joinCmds(spec.Start), ";")
			for _, want := range tt.contains {
				assert.Contains(t, start, want)
			}
			assert.Contains(t, joinCmds(spec.Start)[len(spec.Start)-1], tt.last)
		})
	}
}

// TestServiceSpec_Unregister_EndsTheRunningInstancePerOS pins that uninstall ends a running
// instance on every OS (Windows delete alone does not), and clears the darwin disabled flag.
func TestServiceSpec_Unregister_EndsTheRunningInstancePerOS(t *testing.T) {
	win := joinCmds(specFor(t, "windows").Unregister)
	require.Len(t, win, 2)
	assert.Contains(t, win[0], "schtasks /End /TN")
	assert.Contains(t, win[1], "schtasks /Delete /F /TN")

	mac := joinCmds(specFor(t, "darwin").Unregister)
	require.Len(t, mac, 2)
	assert.Contains(t, mac[0], "launchctl enable", "uninstall clears a disabled flag left by stop")
	assert.Contains(t, mac[1], "launchctl bootout")

	linux := joinCmds(specFor(t, "linux").Unregister)
	assert.Contains(t, linux[0], "disable --now")
}

// TestServiceSpec_Register_EnablesBeforeBootstrap pins that a re-install after a stop works:
// a disabled launchd label refuses bootstrap.
func TestServiceSpec_Register_EnablesBeforeBootstrap(t *testing.T) {
	reg := joinCmds(specFor(t, "darwin").Register)
	require.Len(t, reg, 3)
	assert.Contains(t, reg[0], "bootout")
	assert.Contains(t, reg[1], "launchctl enable")
	assert.Contains(t, reg[2], "launchctl bootstrap")
}

// TestServiceSpec_Status_CanProveRunningAndNotLoadedPerOS pins the status proofs.
func TestServiceSpec_Status_CanProveRunningAndNotLoadedPerOS(t *testing.T) {
	assert.Equal(t, 113, specFor(t, "darwin").NotLoadedExit)
	assert.Equal(t, "state = running", specFor(t, "darwin").RunningMarker)
	assert.Equal(t, 4, specFor(t, "linux").NotLoadedExit)
	assert.Equal(t, 1, specFor(t, "windows").NotLoadedExit)
	assert.Equal(t, `"Running"`, specFor(t, "windows").RunningMarker)
	assert.Contains(t, joinCmds(specFor(t, "windows").Status)[0], "/FO CSV /NH")
}
