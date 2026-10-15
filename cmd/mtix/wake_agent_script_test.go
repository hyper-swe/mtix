// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

// These tests exercise coalescing and drain behavior with a real CLI and owned harness.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWakeAgentScript_BurstTwoEvents20ms_OneLaunchBothHandledOnce(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "First event")
	first := f.start(t, "WAKE_HARNESS_GATE=1", "WAKE_HARNESS_REFRESH=1")
	first.waitFile(t, "started")
	time.Sleep(20 * time.Millisecond)
	f.event(t, "Second event")
	second := f.start(t, "WAKE_HARNESS_REFRESH=1")
	// The other wake must coalesce while the holder is still running.
	require.NoError(t, second.wait(t), "second wake: %s", second.output)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "continue"), []byte("continue"), 0600))
	require.NoError(t, first.wait(t), "holder: %s", first.output)
	assertWakeHandled(t, f, 2, 1)
}

func TestWakeAgentScript_EventMidRun_HandledBeforeRelease(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "First event")
	first := f.start(t, "WAKE_HARNESS_GATE=1")
	first.waitFile(t, "started")
	f.event(t, "Event during run")
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "continue"), []byte("continue"), 0600))
	require.NoError(t, first.wait(t), "holder: %s", first.output)
	assertWakeHandled(t, f, 2, 2)
}

func assertWakeHandled(t *testing.T, f wakeScriptFixture, events, launches int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, "calls"))
	require.NoError(t, err)
	assert.Len(t, strings.Fields(string(raw)), launches)
	acked, err := os.ReadFile(filepath.Join(f.root, "acked"))
	require.NoError(t, err)
	seqs := strings.Fields(string(acked))
	assert.Len(t, seqs, events)
	seen := map[string]bool{}
	for _, seq := range seqs {
		assert.False(t, seen[seq], "event handled once")
		seen[seq] = true
	}
	var inbox []any
	require.NoError(t, json.Unmarshal(f.cli(t, "inbox", "--agent", "TEST-agent", "--json"), &inbox))
	assert.Empty(t, inbox)
}

func TestWakeAgentScript_MissingHarness_FailsLoudly(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "Unhandled event")
	process := f.start(t, "HARNESS_BIN="+filepath.Join(f.root, "missing-harness"))
	err := process.wait(t)
	require.Error(t, err)
	assert.Equal(t, 127, process.cmd.ProcessState.ExitCode())
	assert.NotEmpty(t, process.output.String())
	log, err := os.ReadFile(filepath.Join(f.root, "logs", "wake.log"))
	require.NoError(t, err)
	assert.NotEmpty(t, log)
	var inbox []any
	require.NoError(t, json.Unmarshal(f.cli(t, "inbox", "--agent", "TEST-agent", "--json"), &inbox))
	assert.Len(t, inbox, 1)
}

func TestWakeAgentScript_MissingMtix_FailsLoudly(t *testing.T) {
	f := newWakeScriptFixture(t)
	process := f.start(t, "MTIX_BIN="+filepath.Join(f.root, "missing-mtix"))
	err := process.wait(t)
	require.Error(t, err)
	assert.Equal(t, 127, process.cmd.ProcessState.ExitCode())
	assert.NotEmpty(t, process.output.String())
	log, err := os.ReadFile(filepath.Join(f.root, "logs", "wake.log"))
	require.NoError(t, err)
	assert.NotEmpty(t, log)
}
