// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package service

import "os"

// fileIdentityOf reports no identity on platforms without a change time and
// file id in the standard library (Windows and others): the pulled-board guard
// then reads and hashes tasks.json on every pass (MTIX-130.20.2).
func fileIdentityOf(os.FileInfo) (ctimeNS int64, ino, dev uint64, ok bool) {
	return 0, 0, 0, false
}
