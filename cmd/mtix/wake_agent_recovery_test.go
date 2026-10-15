// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

// Recovery tests preserve pending events when a wake cannot continue.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWakeAgentScript_AcquireFails_ReportsExitOne(t *testing.T) {
	f := newWakeScriptFixture(t)
	wrapper := filepath.Join(f.root, "unavailable-store")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s\\n' 'store unavailable' >&2\nexit 4\n"), 0700))
	p := f.start(t, "MTIX_BIN="+wrapper)
	require.Error(t, p.wait(t))
	assert.Equal(t, 1, p.cmd.ProcessState.ExitCode())
	assert.Contains(t, p.output.String(), "lease acquisition failed")
}

func TestWakeAgentScript_OwnershipReleased_StopsWithoutRelaunch(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "First event")
	wrapper := filepath.Join(f.root, "record-mtix")
	text := "#!/bin/sh\nif [ \"$1 $2 $3\" = 'inbox lease acquire' ]; then\n TOKEN=$(\"$WAKE_REAL_MTIX\" \"$@\") || exit $?\n printf '%s\\n' \"$TOKEN\" > \"$WAKE_DIR/token\"\n printf '%s\\n' \"$TOKEN\"\nelse\n exec \"$WAKE_REAL_MTIX\" \"$@\"\nfi\n"
	require.NoError(t, os.WriteFile(wrapper, []byte(text), 0700))
	p := f.start(t, "WAKE_HARNESS_GATE=1", "MTIX_BIN="+wrapper, "WAKE_REAL_MTIX="+f.binary)
	p.waitFile(t, "started")
	token, err := os.ReadFile(filepath.Join(f.root, "token"))
	require.NoError(t, err)
	f.cli(t, "inbox", "lease", "release", "--agent", "TEST-agent", "--holder", strings.TrimSpace(string(token)), "--abandon")
	replacement := strings.TrimSpace(string(f.cli(t, "inbox", "lease", "acquire", "--agent", "TEST-agent")))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "continue"), []byte("continue"), 0600))
	require.Error(t, p.wait(t))
	assert.Equal(t, 1, p.cmd.ProcessState.ExitCode())
	assert.Contains(t, p.output.String(), "lease release failed")
	assert.Equal(t, replacement, strings.TrimSpace(string(f.cli(t, "inbox", "lease", "acquire", "--agent", "TEST-agent", "--holder", replacement))))
}

func TestWakeAgentScript_DrainBound_LeavesPendingAndAbandons(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "First event")
	p := f.start(t, "WAKE_HARNESS_GATE=1", "WAKE_MAX_RUNS=1")
	p.waitFile(t, "started")
	f.event(t, "New event")
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "continue"), []byte("continue"), 0600))
	require.Error(t, p.wait(t))
	assert.Equal(t, 1, p.cmd.ProcessState.ExitCode())
	assert.Contains(t, p.output.String(), "drain limit reached")
	calls, err := os.ReadFile(filepath.Join(f.root, "calls"))
	require.NoError(t, err)
	assert.Len(t, strings.Fields(string(calls)), 1)
	assert.Contains(t, string(f.cli(t, "inbox", "--agent", "TEST-agent", "--json")), "New event")
	f.cli(t, "inbox", "lease", "acquire", "--agent", "TEST-agent")
}
