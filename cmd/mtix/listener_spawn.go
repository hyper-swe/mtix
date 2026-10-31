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
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer func() { _ = null.Close() }()                                      // best effort: the child holds its own descriptor
	cmd := exec.CommandContext(context.Background(), exe, listenerArgv()...) //nolint:gosec // exe is this process's own binary (os.Executable)
	cmd.Dir = projectDir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.SysProcAttr = listenerSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s daemon --listen: %w", exe, err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release the listener process: %w", err)
	}
	return nil
}
