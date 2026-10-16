// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

// Lifecycle fixtures record interval selection and check owned-process cleanup.

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWakeAgentScript_DefaultDrainBound_StopsAtTwenty(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "First event")
	p := f.start(t, "WAKE_HARNESS_REFILL=1")
	require.Error(t, p.wait(t), "wake: %s", p.output)
	assert.Equal(t, 1, p.cmd.ProcessState.ExitCode())
	assert.Contains(t, p.output.String(), "drain limit reached")
	calls, err := os.ReadFile(filepath.Join(f.root, "calls"))
	require.NoError(t, err)
	assert.Len(t, strings.Split(strings.TrimSpace(string(calls)), "\n"), 20)
	assert.Contains(t, string(f.cli(t, "inbox", "--agent", "TEST-agent", "--json")), "Continuing event")
	f.cli(t, "inbox", "lease", "acquire", "--agent", "TEST-agent")
}

func TestWakeAgentScript_Terminated_AbandonsAndReaps(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "Unhandled event")
	p := f.start(t, "WAKE_HARNESS_GATE=1")
	waitWakeFile(t, p, "started")
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	require.Error(t, p.wait(t), "wake: %s", p.output)
	assert.Equal(t, 1, p.cmd.ProcessState.ExitCode())
	assert.Contains(t, p.output.String(), "wake interrupted")
	assert.Contains(t, string(f.cli(t, "inbox", "--agent", "TEST-agent", "--json")), "Unhandled event")
	f.cli(t, "inbox", "lease", "acquire", "--agent", "TEST-agent")
}

func TestWakeAgentScript_ChildFails_ReleasesAfterHandling(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "Handled event")
	p := f.start(t, "WAKE_HARNESS_FAIL=1")
	require.NoError(t, p.wait(t), "wake: %s", p.output)
	assert.Contains(t, p.output.String(), "harness exited (7)")
	assertWakeHandled(t, f, 1, 1)
	f.cli(t, "inbox", "lease", "acquire", "--agent", "TEST-agent")
}

func TestWakeAgentScript_EmptyInbox_DoesNotLaunch(t *testing.T) {
	f := newWakeScriptFixture(t)
	p := f.start(t)
	require.NoError(t, p.wait(t), "wake: %s", p.output)
	_, err := os.Stat(filepath.Join(f.root, "calls"))
	require.ErrorIs(t, err, os.ErrNotExist)
	f.cli(t, "inbox", "lease", "acquire", "--agent", "TEST-agent")
}

func TestWakeAgentScript_Renewal_UsesSameHolderAndSixtySeconds(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "First event")
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "sleep"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$WAKE_DIR/intervals\"\nexec /bin/sleep 0.1\n"), 0700))
	wrapper := filepath.Join(f.root, "record-mtix")
	text := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$WAKE_DIR/operations\"\nif [ \"$4\" = '--holder' ] || [ \"${6:-}\" = '--holder' ]; then : > \"$WAKE_DIR/renewed\"; fi\nexec \"$WAKE_REAL_MTIX\" \"$@\"\n"
	require.NoError(t, os.WriteFile(wrapper, []byte(text), 0700))
	p := f.start(t, "WAKE_HARNESS_GATE=1", "MTIX_BIN="+wrapper, "WAKE_REAL_MTIX="+f.binary)
	waitWakeFile(t, p, "started")
	waitWakeFile(t, p, "renewed")
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "continue"), []byte("continue"), 0600))
	require.NoError(t, p.wait(t), "wake: %s", p.output)
	intervals, err := os.ReadFile(filepath.Join(f.root, "intervals"))
	require.NoError(t, err)
	for _, interval := range strings.Fields(string(intervals)) {
		assert.Equal(t, "60", interval)
	}
	operations, err := os.ReadFile(filepath.Join(f.root, "operations"))
	require.NoError(t, err)
	var token string
	for _, line := range strings.Split(string(operations), "\n") {
		fields := strings.Fields(line)
		for i, arg := range fields {
			if arg == "--holder" {
				if token == "" {
					token = fields[i+1]
				}
				assert.Equal(t, token, fields[i+1])
			}
		}
	}
	assert.Regexp(t, "^[0-9a-f]{32}$", token)
	assertWakeHandled(t, f, 1, 1)
}

func TestWakeAgentScript_RenewalLosesOwnership_StopsAndKeepsReplacement(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "Pending event")
	// The owned interval stub blocks until replacement, then retains the 60s argument.
	text := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$WAKE_DIR/intervals\"\nwhile [ ! -f \"$WAKE_DIR/renew-now\" ]; do /bin/sleep 0.01; done\n"
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "sleep"), []byte(text), 0700))
	wrapper := filepath.Join(f.root, "record-mtix")
	text = "#!/bin/sh\nif [ \"$1 $2 $3\" = 'inbox lease acquire' ]; then\n TOKEN=$(\"$WAKE_REAL_MTIX\" \"$@\") || exit $?\n printf '%s\\n' \"$TOKEN\" > \"$WAKE_DIR/token\"\n printf '%s\\n' \"$TOKEN\"\nelse\n exec \"$WAKE_REAL_MTIX\" \"$@\"\nfi\n"
	require.NoError(t, os.WriteFile(wrapper, []byte(text), 0700))
	p := f.start(t, "WAKE_HARNESS_GATE=1", "MTIX_BIN="+wrapper, "WAKE_REAL_MTIX="+f.binary)
	waitWakeFile(t, p, "started")
	token, err := os.ReadFile(filepath.Join(f.root, "token"))
	require.NoError(t, err)
	f.cli(t, "inbox", "lease", "release", "--agent", "TEST-agent", "--holder", strings.TrimSpace(string(token)), "--abandon")
	replacement := strings.TrimSpace(string(f.cli(t, "inbox", "lease", "acquire", "--agent", "TEST-agent")))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "renew-now"), []byte("ready"), 0600))
	require.Error(t, p.wait(t), "wake: %s", p.output)
	assert.Equal(t, 1, p.cmd.ProcessState.ExitCode())
	assert.Contains(t, p.output.String(), "lease no longer owned")
	assert.Equal(t, replacement, strings.TrimSpace(string(f.cli(t, "inbox", "lease", "acquire", "--agent", "TEST-agent", "--holder", replacement))))
	assert.Contains(t, string(f.cli(t, "inbox", "--agent", "TEST-agent", "--json")), "Pending event")
}
