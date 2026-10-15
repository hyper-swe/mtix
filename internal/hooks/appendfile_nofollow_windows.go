// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package hooks

import "os"

// Windows has no non-blocking open flag; containment is enforced by the root
// handle, and the final component by the exclusive create and the post-open
// same-file check.
const (
	nonBlockFlag = 0
)

func clearNonBlock(*os.File) error { return nil }

// linkCount reports 1: hard links are not detected on Windows.
func linkCount(os.FileInfo) uint64 { return 1 }

// wideMode is always false: Windows permission bits do not describe access.
func wideMode(os.FileInfo) bool { return false }
