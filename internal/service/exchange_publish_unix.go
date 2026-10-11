// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Native receipt publication uses same-directory rename and directory policy.
package service

import (
	"os"
	"syscall"
)

func exchangePlatformSupported() bool { return true }
func lockExchangeReader(f *os.File) error {
	return syscall.Flock(fileFd(f), lockShared|syscall.LOCK_NB)
}
func publishExchangeFile(from, to string) error { return os.Rename(from, to) }
