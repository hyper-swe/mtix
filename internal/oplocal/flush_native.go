// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"errors"
	"os"
)

func flushStateFile(f *os.File, flush func(*os.File) error) error {
	if flush == nil {
		flush = platformFlush
	}
	if err := flush(f); err != nil {
		return nativeFlushError{cause: err}
	}
	return nil
}

// Control holds the descriptor valid across the native operation and its retries.
func withFileHandle(f *os.File, flush func(uintptr) error) error {
	conn, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var nativeErr error
	controlErr := conn.Control(func(fd uintptr) { nativeErr = flush(fd) })
	// Match File.Sync's closed-file classification while retaining the raw cause.
	if controlErr != nil && f.Fd() == ^uintptr(0) {
		controlErr = errors.Join(controlErr, os.ErrClosed)
	}
	return errors.Join(controlErr, nativeErr)
}
