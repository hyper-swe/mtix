// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteServiceUnit_WritesFileWhenPlatformNeedsOne: the unit/plist is
// persisted with its parent dir created (launchd/systemd path).
func TestWriteServiceUnit_WritesFileWhenPlatformNeedsOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "mtix.plist")
	var out bytes.Buffer
	require.NoError(t, writeServiceUnit(&out, &serviceSpec{Path: path, Content: "UNIT-BODY"}))

	got, err := os.ReadFile(path) //nolint:gosec // test path
	require.NoError(t, err)
	assert.Equal(t, "UNIT-BODY", string(got))
	assert.Contains(t, out.String(), path, "the written path is echoed")
}

// TestWriteServiceUnit_NoFileWhenPlatformNeedsNone: Task Scheduler has no unit
// file (empty Path) — writing is a clean no-op.
func TestWriteServiceUnit_NoFileWhenPlatformNeedsNone(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, writeServiceUnit(&out, &serviceSpec{Path: "", Content: "x"}))
	assert.Empty(t, out.String())
}

// TestRunServiceCmds_LenientToleratesNonFinalFailure: with lenient=true a
// failing pre-clean (e.g. bootout of an unloaded label) is skipped as long as
// the LAST command succeeds — the idempotent-install contract (MTIX-56.3).
func TestRunServiceCmds_LenientToleratesNonFinalFailure(t *testing.T) {
	var out bytes.Buffer
	err := runServiceCmds(context.Background(), &out, [][]string{
		{"false"}, // pre-clean that "fails"
		{"true"},  // the real command
	}, true)
	require.NoError(t, err, "a non-final failure is tolerated under lenient")
	assert.Contains(t, out.String(), "$ false")
	assert.Contains(t, out.String(), "$ true")
}

// TestRunServiceCmds_StrictPropagatesFailure: without lenient, any failure
// surfaces (start/stop/status must not swallow errors).
func TestRunServiceCmds_StrictPropagatesFailure(t *testing.T) {
	var out bytes.Buffer
	err := runServiceCmds(context.Background(), &out, [][]string{{"false"}}, false)
	require.Error(t, err)
}

// TestRunServiceCmds_LenientStillFailsOnLastCommand: the final command's error
// is always surfaced, even under lenient.
func TestRunServiceCmds_LenientStillFailsOnLastCommand(t *testing.T) {
	var out bytes.Buffer
	err := runServiceCmds(context.Background(), &out, [][]string{{"true"}, {"false"}}, true)
	require.Error(t, err, "the last command failing is fatal even under lenient")
}
