// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package procinfo_test

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/procinfo"
)

func TestStartTime_OwnProcess_IsStableAndNonEmpty(t *testing.T) {
	first, err := procinfo.StartTime(os.Getpid())
	if errors.Is(err, procinfo.ErrUnsupported) {
		t.Skipf("no process start time on %s", runtime.GOOS)
	}
	require.NoError(t, err)
	require.NotEmpty(t, first)
	second, err := procinfo.StartTime(os.Getpid())
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

func TestStartTime_ExitedProcess_ReturnsErrNoProcess(t *testing.T) {
	if _, err := procinfo.StartTime(os.Getpid()); errors.Is(err, procinfo.ErrUnsupported) {
		t.Skipf("no process start time on %s", runtime.GOOS)
	}
	cmd := exec.Command(os.Args[0], "-test.run=NONE")
	require.NoError(t, cmd.Run())
	_, err := procinfo.StartTime(cmd.Process.Pid)
	assert.ErrorIs(t, err, procinfo.ErrNoProcess)
}

func TestStartTime_InvalidPID_ReturnsErrNoProcess(t *testing.T) {
	if _, err := procinfo.StartTime(os.Getpid()); errors.Is(err, procinfo.ErrUnsupported) {
		t.Skipf("no process start time on %s", runtime.GOOS)
	}
	_, err := procinfo.StartTime(0)
	assert.ErrorIs(t, err, procinfo.ErrNoProcess)
	_, err = procinfo.StartTime(-5)
	assert.ErrorIs(t, err, procinfo.ErrNoProcess)
}
