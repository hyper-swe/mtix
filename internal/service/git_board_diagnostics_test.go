//go:build linux || darwin

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Git failures expose bounded fixed hints, never subprocess or repository text.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeBaseBoard_Diagnostics_ExitAndStderrArePrivate(t *testing.T) {
	for _, tc := range []struct{ name, stderr, hint string }{
		{"empty", "", "empty"},
		{"unknown", "PRIVATE_CONFIG PRIVATE_FILE /PRIVATE_PATH", "unknown"},
		{"option", "unknown option: PRIVATE_CONFIG\nPRIVATE_FILE", "unsupported-option"},
		{"reference", "fatal: no upstream configured for branch 'PRIVATE_CONFIG'\nPRIVATE_FILE", "reference-unavailable"},
		{"object", "fatal: path 'PRIVATE_FILE' does not exist in 'PRIVATE_CONFIG'", "object-unavailable"},
		{"lock", "fatal: Unable to create '/PRIVATE_PATH/index.lock': File exists.\nPRIVATE_CONFIG", "lock-conflict"},
		{"spoofed", "fatal: PRIVATE_CONFIG\nerror: PRIVATE_FILE", "unknown"},
		{"binary", "PRIVATE_CONFIG\x00\x1b[31m\xffPRIVATE_FILE", "unknown"},
		{"bounded", strings.Repeat("PRIVATE_FILE", 1300), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := filepath.Join(t.TempDir(), "PRIVATE_FILE")
			boardWrite(t, raw, []byte(tc.stderr))
			path := boardScript(t, "/bin/cat '"+raw+"' >&2; exit 7")
			_, err := readGit(t.Context(), path, t.TempDir(), 1024, "merge-base")
			assertBoardDiagnostic(t, err, boardDiagnosticExpectation{"merge-base", "exit", "7", "none", tc.hint})
		})
	}
}

type boardDiagnosticExpectation struct{ operation, process, status, cleanup, stderr string }

func assertBoardDiagnostic(t *testing.T, err error, expected boardDiagnosticExpectation) {
	t.Helper()
	require.ErrorIs(t, err, ErrNoMergeBase)
	want := "ancestor board unavailable: Git operation failed: operation=" + expected.operation + "; process=" + expected.process + "; exit=" + expected.status + "; cleanup=" + expected.cleanup + "; stderr=" + expected.stderr
	require.Equal(t, want, err.Error())
	require.Less(t, len(err.Error()), 256)
	encoded, marshalErr := json.Marshal(struct{ Error string }{err.Error()})
	require.NoError(t, marshalErr)
	for _, rendered := range []string{err.Error(), fmt.Sprintf("%v %+v %#v", err, err, err), string(encoded)} {
		for _, secret := range []string{"PRIVATE_CONFIG", "PRIVATE_FILE", "PRIVATE_PATH", "PRIVATE_ARG"} {
			require.NotContains(t, rendered, secret)
		}
	}
	var rawExit *exec.ExitError
	var rawPath *os.PathError
	require.False(t, errors.As(err, &rawExit))
	require.False(t, errors.As(err, &rawPath))
}

func TestMergeBaseBoard_Diagnostics_OperationAndLaunchAreFinite(t *testing.T) {
	for _, operation := range []string{"show", "PRIVATE_ARG", ""} {
		path := filepath.Join(t.TempDir(), "PRIVATE_PATH")
		_, err := readGit(t.Context(), path, t.TempDir(), 1024, operation)
		want := "unknown"
		if operation == "show" {
			want = "board-show"
		}
		assertBoardDiagnostic(t, err, boardDiagnosticExpectation{want, "launch-not-found", "unavailable", "none", "empty"})
	}
	path := filepath.Join(t.TempDir(), "PRIVATE_PATH")
	boardWrite(t, path, []byte("PRIVATE_FILE"))
	_, err := readGit(t.Context(), path, t.TempDir(), 1024, "show")
	assertBoardDiagnostic(t, err, boardDiagnosticExpectation{"board-show", "launch-permission", "unavailable", "none", "empty"})
}

func TestMergeBaseBoard_Diagnostics_SignalAndPipeWaitAreDistinct(t *testing.T) {
	t.Run("signal", func(t *testing.T) {
		_, err := readGit(t.Context(), boardScript(t, "kill -TERM $$"), t.TempDir(), 1024, "show")
		assertBoardDiagnostic(t, err, boardDiagnosticExpectation{"board-show", "signal", "unavailable", "none", "empty"})
	})
	t.Run("pipe", func(t *testing.T) {
		gate := filepath.Join(t.TempDir(), "release")
		require.NoError(t, syscall.Mkfifo(gate, 0600))
		// The child retains the pipes until group cleanup; its FIFO open blocks on an event.
		body := "/bin/sh -c \"read value < '" + gate + "'\" & exit 0"
		_, err := readGit(t.Context(), boardScript(t, body), t.TempDir(), 1024, "show")
		assertBoardDiagnostic(t, err, boardDiagnosticExpectation{"board-show", "pipe-wait-delay", "0", "none", "empty"})
	})
}

func TestMergeBaseBoard_Diagnostics_PrecedenceAndIdentity(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"stdout", "printf '12345'; printf PRIVATE_FILE >&2; exit 7", "output size limit"},
		{"stderr", "printf '%s' '" + strings.Repeat("x", boardStderrLimit+1) + "' >&2; exit 7", "output size limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readGit(t.Context(), boardScript(t, tc.body), t.TempDir(), 4, "show")
			require.ErrorIs(t, err, ErrNoMergeBase)
			require.Equal(t, "ancestor board unavailable: "+tc.want, err.Error())
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := readGit(ctx, filepath.Join(t.TempDir(), "PRIVATE_PATH"), t.TempDir(), 1024, "show")
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "ancestor board unavailable: operation canceled: context canceled", err.Error())
	for _, cause := range []error{errors.New("PRIVATE_FILE"), fmt.Errorf("PRIVATE_CONFIG: %w", context.DeadlineExceeded)} {
		err = gitBoardError("fixed reason", cause)
		require.ErrorIs(t, err, ErrNoMergeBase)
		require.NotContains(t, fmt.Sprintf("%+v %#v", err, err), "PRIVATE_")
		if errors.Is(cause, context.DeadlineExceeded) {
			require.ErrorIs(t, err, context.DeadlineExceeded)
		}
	}
}

func TestMergeBaseBoard_Diagnostics_ProcessAndCleanupHintsRemainPrivate(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "exit 0")
	require.NoError(t, cmd.Run())
	for _, tc := range []struct {
		name, process, exit, cleanup string
		runErr, cleanupErr           error
		state                        *os.ProcessState
	}{
		{"cleanup", "completed", "0", "failed", nil, errors.New("PRIVATE_PATH"), cmd.ProcessState},
		{"both", "pipe-wait-delay", "0", "failed", exec.ErrWaitDelay, errors.New("PRIVATE_PATH"), cmd.ProcessState},
		{"unknown-process", "unknown", "0", "none", errors.New("PRIVATE_FILE"), nil, cmd.ProcessState},
		{"other-launch", "launch-other", "unavailable", "none", errors.New("PRIVATE_PATH"), nil, nil},
		{"lookup", "launch-not-found", "unavailable", "none", exec.ErrNotFound, nil, nil},
		{"wrapped-permission", "launch-permission", "unavailable", "none", fmt.Errorf("PRIVATE_PATH: %w", os.ErrPermission), nil, nil},
		{"exit-without-state", "exit", "unavailable", "none", &exec.ExitError{Stderr: []byte("PRIVATE_FILE")}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := boardGitFailure(&exec.Cmd{ProcessState: tc.state}, tc.runErr, tc.cleanupErr, []byte("PRIVATE_CONFIG"), nil)
			assertBoardDiagnostic(t, err, boardDiagnosticExpectation{"unknown", tc.process, tc.exit, tc.cleanup, "unknown"})
		})
	}
	// Even malformed internal indices and maximal numeric values render finitely.
	err := gitBoardError("Git operation failed", boardGitDiagnostic{operation: 255, process: 255, stderr: 255, exit: math.MaxInt})
	assertBoardDiagnostic(t, err, boardDiagnosticExpectation{"unknown", "unknown", fmt.Sprint(math.MaxInt), "none", "unknown"})
}
