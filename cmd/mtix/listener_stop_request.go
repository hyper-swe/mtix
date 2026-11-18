// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// listenerStopFilename is the stop request `mtix daemon stop --listener`
// leaves for the detached listener (MTIX-276). Only listen mode reads it
// (daemonListener.Done), so a service or foreground daemon ignores it.
const listenerStopFilename = "listener.stop"

func listenerStopPath(mtixDir string) string {
	return filepath.Join(mtixDir, "data", listenerStopFilename)
}

// listenerStopRequested reports whether a regular stop request file exists. A
// symlink or directory at the path is not a request.
func listenerStopRequested(mtixDir string) bool {
	info, err := os.Lstat(listenerStopPath(mtixDir))
	return err == nil && info.Mode().IsRegular()
}

// writeListenerStopRequest creates the request file (mode 0600). It refuses
// to write through a symlink or over anything that is not a regular file.
func writeListenerStopRequest(mtixDir string) error {
	path := listenerStopPath(mtixDir)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; remove it and retry", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// clearListenerStopRequest removes a regular request file; absent is fine.
func clearListenerStopRequest(mtixDir string) error {
	path := listenerStopPath(mtixDir)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; remove it by hand", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
