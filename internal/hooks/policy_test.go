// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExecDispatchMode_DefaultsToAny: an unset policy (no file) is "any" —
// current behavior, never a silently dead fabric (MTIX-56.10).
func TestExecDispatchMode_DefaultsToAny(t *testing.T) {
	assert.Equal(t, ExecDispatchAny, ExecDispatchMode(t.TempDir()))
}

// TestExecDispatchMode_RoundTripsValidModes: each valid mode saves and reads
// back, tolerant of trailing whitespace/newline.
func TestExecDispatchMode_RoundTripsValidModes(t *testing.T) {
	for _, mode := range []string{ExecDispatchAny, ExecDispatchDaemon, ExecDispatchOff} {
		dir := t.TempDir()
		require.NoError(t, SaveExecDispatchMode(dir, mode))
		assert.Equal(t, mode, ExecDispatchMode(dir), "round-trip %q", mode)
	}
}

// TestExecDispatchMode_UnknownContentFailsOpen: a garbage or partially-written
// policy file reads as "any" rather than disabling dispatch.
func TestExecDispatchMode_UnknownContentFailsOpen(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, dispatchPolicyFileName), []byte("garbage\n"), 0o600))
	assert.Equal(t, ExecDispatchAny, ExecDispatchMode(dir))
}

// TestSaveExecDispatchMode_RejectsInvalid: an unknown mode is refused, so a
// typo never silently persists a meaningless policy.
func TestSaveExecDispatchMode_RejectsInvalid(t *testing.T) {
	err := SaveExecDispatchMode(t.TempDir(), "sometimes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sometimes")
}

// TestExecAdapter_NameIsExecKey: the adapter registers under the exec deliver
// key (guards the registry wiring).
func TestExecAdapter_NameIsExecKey(t *testing.T) {
	assert.Equal(t, AdapterExec, NewExecAdapter().Name())
}
