// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

// Reporting tests retain operator directory modes and reject invalid bounds early.

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestWakeAgentScript_LogDirectory_PreservesExistingMode(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "Missing"
		if existing {
			name = "Existing"
		}
		t.Run(name, func(t *testing.T) {
			f := newWakeScriptFixture(t)
			dir := filepath.Join(f.root, "logs")
			if existing {
				require.NoError(t, os.Mkdir(dir, 0755))
				require.NoError(t, os.Chmod(dir, 0755))
			}
			p := f.start(t, "MTIX_BIN="+filepath.Join(f.root, "missing-tool"))
			assert.Error(t, p.wait(t))
			assert.Equal(t, 127, p.cmd.ProcessState.ExitCode())
			info, err := os.Stat(dir)
			require.NoError(t, err)
			expected := os.FileMode(0700)
			if existing {
				expected = 0755
			}
			assert.Equal(t, expected, info.Mode().Perm())
			log, err := os.ReadFile(filepath.Join(dir, "wake.log"))
			assert.NoError(t, err)
			assert.Contains(t, string(log), "mtix executable unavailable")
		})
	}
}

func TestWakeAgentScript_InvalidDrainLimit_ReportsBeforeAcquire(t *testing.T) {
	for _, value := range []string{"0", "invalid", "-1"} {
		t.Run(value, func(t *testing.T) {
			f := newWakeScriptFixture(t)
			marker := filepath.Join(f.root, "acquire-called")
			wrapper := wakeProtocolWrapper(t, f, "printf '%s\\n' called > \"$WAKE_DIR/acquire-called\"")
			p := f.start(t, "WAKE_MAX_RUNS="+value, "MTIX_BIN="+wrapper, "WAKE_REAL_MTIX="+f.binary)
			assert.Error(t, p.wait(t))
			assert.Equal(t, 1, p.cmd.ProcessState.ExitCode())
			assert.Contains(t, p.output.String(), "WAKE_MAX_RUNS must be a positive integer")
			log, err := os.ReadFile(filepath.Join(f.root, "logs", "wake.log"))
			assert.NoError(t, err)
			assert.Contains(t, string(log), "WAKE_MAX_RUNS must be a positive integer")
			_, err = os.Stat(marker)
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
