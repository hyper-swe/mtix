// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-56.3: daemon-as-service. buildServiceSpec is pure, so every platform's
// generation is tested on every platform; the register/start/stop verbs shell
// out to the OS and are exercised on real hosts, not in CI.
package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildServiceSpec_Darwin(t *testing.T) {
	spec, err := buildServiceSpec("darwin", "/usr/local/bin/mtix", "/work/proj", "/Users/u", 5)
	require.NoError(t, err)

	assert.Contains(t, spec.Path, "/Users/u/Library/LaunchAgents/")
	assert.Contains(t, spec.Path, ".plist")
	assert.Contains(t, spec.Content, "<key>KeepAlive</key><true/>", "launchd restarts the daemon on crash")
	assert.Contains(t, spec.Content, "<key>RunAtLoad</key><true/>", "starts at login")
	assert.Contains(t, spec.Content, "<string>/usr/local/bin/mtix</string>")
	assert.Contains(t, spec.Content, "<string>-C</string>")
	assert.Contains(t, spec.Content, "<string>/work/proj</string>", "the service targets the project via the global -C")
	assert.Contains(t, spec.Content, "daemon.err.log", "logs land under .mtix/logs")

	require.NotEmpty(t, spec.Register)
	assert.Equal(t, "bootout", spec.Register[0][1], "idempotent install pre-cleans the label before bootstrap")
	assert.Equal(t, "bootstrap", spec.Register[len(spec.Register)-1][1])
}

func TestBuildServiceSpec_Linux(t *testing.T) {
	spec, err := buildServiceSpec("linux", "/usr/bin/mtix", "/work/proj", "/home/u", 5)
	require.NoError(t, err)

	assert.Contains(t, spec.Path, "/home/u/.config/systemd/user/")
	assert.Contains(t, spec.Content, "Restart=always", "systemd restarts the daemon on crash")
	assert.Contains(t, spec.Content, "WantedBy=default.target", "boot-start for the user session")
	assert.Contains(t, spec.Content, "/usr/bin/mtix -C /work/proj daemon")
	joined := ""
	for _, c := range spec.Register {
		joined += strings.Join(c, " ") + ";"
	}
	assert.Contains(t, joined, "daemon-reload")
	assert.Contains(t, joined, "enable --now", "install enables AND starts")
}

func TestBuildServiceSpec_Windows(t *testing.T) {
	spec, err := buildServiceSpec("windows", `C:\mtix.exe`, `C:\work\proj`, `C:\Users\u`, 5)
	require.NoError(t, err)

	assert.Empty(t, spec.Path, "Task Scheduler needs no unit file")
	require.NotEmpty(t, spec.Register)
	reg := strings.Join(spec.Register[0], " ")
	assert.Contains(t, reg, "schtasks /Create /F", "/F makes re-install idempotent")
	assert.Contains(t, reg, "ONLOGON")
	assert.Contains(t, reg, "daemon")
}

func TestBuildServiceSpec_PerProjectLabels(t *testing.T) {
	a, err := buildServiceSpec("darwin", "/bin/mtix", "/work/a", "/Users/u", 5)
	require.NoError(t, err)
	b, err := buildServiceSpec("darwin", "/bin/mtix", "/work/b", "/Users/u", 5)
	require.NoError(t, err)
	assert.NotEqual(t, a.Label, b.Label, "one service per project — labels must not collide")
}

func TestBuildServiceSpec_UnsupportedPlatform(t *testing.T) {
	_, err := buildServiceSpec("plan9", "/bin/mtix", "/w", "/h", 5)
	require.Error(t, err)
}

func TestDaemonCmd_HasServiceSubcommands(t *testing.T) {
	cmd := newDaemonCmd()
	want := map[string]bool{"install": false, "uninstall": false, "start": false, "stop": false, "status": false}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		assert.Truef(t, found, "mtix daemon %s declared (MTIX-56.3)", name)
	}
}
