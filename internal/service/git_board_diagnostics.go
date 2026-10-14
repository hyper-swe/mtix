// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Diagnostics contain only finite hints and numeric exit status, never raw Git text.
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// The stored indices cannot retain repository text or underlying process errors.
// Stderr categories are diagnostic hints: hostile text can imitate Git messages.
type boardGitDiagnostic struct {
	operation uint8
	process   uint8
	stderr    uint8
	exit      int
	cleanup   bool
}

func (d boardGitDiagnostic) Error() string {
	operation := boardDiagnosticLabel(d.operation, []string{"unknown", "merge-base", "board-show"})
	process := boardDiagnosticLabel(d.process, []string{"unknown", "completed", "exit", "signal", "pipe-wait-delay", "launch-not-found", "launch-permission", "launch-other"})
	stderr := boardDiagnosticLabel(d.stderr, []string{"unknown", "empty", "unsupported-option", "reference-unavailable", "object-unavailable", "lock-conflict"})
	status, cleanup := "unavailable", "none"
	if d.exit >= 0 {
		status = strconv.Itoa(d.exit)
	}
	if d.cleanup {
		cleanup = "failed"
	}
	return fmt.Sprintf("operation=%s; process=%s; exit=%s; cleanup=%s; stderr=%s", operation, process, status, cleanup, stderr)
}

func boardDiagnosticLabel(index uint8, labels []string) string {
	if int(index) >= len(labels) {
		return "unknown"
	}
	return labels[index]
}

func boardGitFailure(cmd *exec.Cmd, runErr, cleanupErr error, stderr []byte, args []string) error {
	d := boardGitDiagnostic{exit: -1, cleanup: cleanupErr != nil, stderr: boardStderrCategory(stderr)}
	if cmd.ProcessState != nil {
		d.exit = cmd.ProcessState.ExitCode()
	}
	if len(args) > 0 {
		switch args[0] {
		case "merge-base":
			d.operation = 1
		case "show":
			d.operation = 2
		}
	}
	d.process = boardProcessCategory(runErr, cmd.ProcessState)
	return gitBoardError("Git operation failed", d)
}

func boardProcessCategory(err error, state *os.ProcessState) uint8 {
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 1
	case errors.Is(err, exec.ErrWaitDelay):
		return 4
	case errors.As(err, &exitErr):
		if state != nil && !state.Exited() {
			return 3
		}
		return 2
	case errors.Is(err, os.ErrNotExist), errors.Is(err, exec.ErrNotFound):
		return 5
	case errors.Is(err, os.ErrPermission):
		return 6
	case state == nil:
		return 7
	default:
		return 0
	}
}

func boardStderrCategory(stderr []byte) uint8 {
	raw := string(stderr)
	switch {
	case len(raw) == 0:
		return 1
	case strings.HasPrefix(raw, "unknown option:"), strings.HasPrefix(raw, "error: unknown option"):
		return 2
	case strings.HasPrefix(raw, "fatal: no upstream configured for branch '"):
		return 3
	case strings.HasPrefix(raw, "fatal: path '") && strings.Contains(raw, "' does not exist in '"):
		return 4
	case strings.HasPrefix(raw, "fatal: Unable to create '") && strings.Contains(raw, ".lock': File exists."):
		return 5
	default:
		return 0
	}
}
