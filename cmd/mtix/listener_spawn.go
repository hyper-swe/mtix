// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// daemonLockHeld reports whether a daemon or listener holds the board's
// lifetime lock. The lock is the authority on whether one runs. The probe is
// read-only: it opens the lock file without creating it, tries the lock and
// closes the file (which releases it); it never writes the PID file, so a
// probe cannot disturb the owner's diagnostics. A starting daemon that meets
// the probe's instant of ownership retries (daemonOwnershipFor).
func daemonLockHeld(mtixDir string) (held bool, err error) {
	file, err := os.OpenFile(filepath.Join(mtixDir, "data", daemonLockFilename), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil // never started: nothing holds it
	}
	if err != nil {
		return false, fmt.Errorf("open daemon lock: %w", err)
	}
	lockErr := platformAcquireDaemonLock(file)
	closeErr := file.Close()
	if errors.Is(lockErr, errDaemonLockHeld) {
		return true, closeErr
	}
	return false, errors.Join(lockErr, closeErr)
}

// listenerArgv is the whole argument list of the spawned listener. It is a
// fixed list: nothing from the environment, config or secrets enters it.
func listenerArgv() []string { return []string{"daemon", "--listen"} }

// spawnListener starts `mtix daemon --listen` detached, with the same binary
// as the waiter, in the project directory. The command line carries no
// argument that names a credential and the environment is inherited
// unchanged: the listener reads its hub settings itself, exactly as the
// daemon does. Its output is discarded. What it did is visible through
// `mtix doctor` (rows sync.hub-contact, sync.listen and
// wake.listener.no-hub-listener); a journal record of its exit reason is a
// follow-up (MTIX-112.20.4 review, S3).
func spawnListener(exe, projectDir string) error {
	return spawnDetached(exe, projectDir, listenerArgv())
}

// spawnDetached starts exe with argv detached (own session or process group,
// no terminal, output discarded) in projectDir, and does not wait for it. The
// environment is inherited unchanged and argv never carries a credential
// (MTIX-112.20.2 reuses it for the push child of an addressed send).
func spawnDetached(exe, projectDir string, argv []string) error {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer func() { _ = null.Close() }() // best effort: the child holds its own descriptor
	cmd := newDetachedCmd(exe, projectDir, argv, null)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s %s: %w", exe, strings.Join(argv, " "), err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release the detached process: %w", err)
	}
	return nil
}

// newDetachedCmd builds the command spawnDetached starts: own session or
// process group, no terminal, all three streams on null. It is separate so a
// test can assert the detachment without starting a process.
func newDetachedCmd(exe, projectDir string, argv []string, null *os.File) *exec.Cmd {
	cmd := exec.CommandContext(context.Background(), exe, argv...) //nolint:gosec // exe is this process's own binary (os.Executable)
	cmd.Dir = projectDir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.SysProcAttr = listenerSysProcAttr()
	return cmd
}
