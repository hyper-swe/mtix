// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

// These checks cover the marker wait on owned wake fixtures.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWakeScriptProcess_AwaitFile_EarlyExit_FailsFastWithOutput(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "First event")
	harness := filepath.Join(f.root, "failing-harness")
	require.NoError(t, os.WriteFile(harness, []byte("#!/bin/sh\necho harness-boom >&2\nexit 3\n"), 0700))
	p := f.start(t, "HARNESS_BIN="+harness, "WAKE_MAX_RUNS=1")
	begin := time.Now()
	err := p.awaitFile("started", 20*time.Second)
	require.Error(t, err)
	assert.Less(t, time.Since(begin), 15*time.Second, "an ended process must not wait for the deadline")
	assert.Contains(t, err.Error(), "ended")
	assert.Contains(t, err.Error(), "harness-boom")
	assert.Contains(t, err.Error(), "token=")
}

func TestWakeScriptProcess_AwaitFile_Deadline_ReportsPhase(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "First event")
	p := f.start(t, "WAKE_HARNESS_GATE=1")
	require.NoError(t, p.awaitFile("started", 30*time.Second))
	err := p.awaitFile("never-created", 300*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not create never-created within 300ms")
	assert.Contains(t, err.Error(), "process: running")
	assert.Contains(t, err.Error(), "started=true")
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "continue"), []byte("continue"), 0600))
	require.NoError(t, p.wait(t))
}

func TestWakeScriptProcess_AwaitFile_Present_Succeeds(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "First event")
	p := f.start(t, "WAKE_HARNESS_GATE=1")
	require.NoError(t, p.awaitFile("started", 30*time.Second))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "continue"), []byte("continue"), 0600))
	require.NoError(t, p.wait(t))
}
