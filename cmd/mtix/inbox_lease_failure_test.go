// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Lease failures preserve identity and output error causes.
package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeaseIdentity_MissingAgentOrHome_RefusesIdentity(t *testing.T) {
	_, err := leaseIdentity("")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	_, err = leaseIdentity("TEST-agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lease identity")
}

func TestLeaseIdentity_UnsafeOperatorDirectory_RefusesHostID(t *testing.T) {
	setupLeaseApp(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("APPDATA", filepath.Join(t.TempDir(), "config"))
	_, err := leaseIdentity("TEST-agent")
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}

func TestRunLeaseOperation_NoProject_ReturnsGuidance(t *testing.T) {
	old := app.store
	app.store = nil
	t.Cleanup(func() { app.store = old })
	err := runLeaseOperation(newInboxCmd(), "acquire", leaseOptions{agent: "TEST-agent", ttl: "15m"})
	require.ErrorContains(t, err, "not in an mtix project")
}

func TestWriteLeaseOutput_ClosedOutput_PreservesWriteError(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "json"}[jsonOutput], func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "output")
			require.NoError(t, err)
			require.NoError(t, file.Close())
			old := os.Stdout
			os.Stdout = file
			t.Cleanup(func() { os.Stdout = old })
			err = writeLeaseOutput(newInboxCmd(), jsonOutput, "value", "value")
			require.ErrorIs(t, err, os.ErrClosed)
		})
	}
}

func TestInboxLeaseCmd_ClosedStore_ReturnsStorageFailure(t *testing.T) {
	for _, verb := range []string{"acquire", "release"} {
		t.Run(verb, func(t *testing.T) {
			setupLeaseApp(t)
			require.NoError(t, app.store.Close())
			_, err := executeLease(t, verb, "--agent", "TEST-agent", "--holder", "0123456789abcdef0123456789abcdef")
			require.Error(t, err)
			assert.NotErrorIs(t, err, errLeaseHeld)
		})
	}
}

func TestPrintFinalError_LeaseDiagnostic_RespectsStoragePriority(t *testing.T) {
	held := &leaseHeldError{agent: "TEST-agent", expires: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC), cause: model.ErrAlreadyClaimed}
	for _, tt := range []struct {
		name   string
		err    error
		prefix string
	}{
		{"held", held, ""}, {"disk-full", errors.Join(held, model.ErrDiskFull), "error: "}, {"corrupt", errors.Join(held, model.ErrCorrupted), "error: "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			printFinalError(&output, tt.err)
			assert.Equal(t, tt.prefix+tt.err.Error()+"\n", output.String())
		})
	}
}
