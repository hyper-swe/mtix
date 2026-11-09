// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package service

// renameNoReplace has no atomic no-replace rename on this platform; the move
// refuses an existing target and the caller re-checks what moved.
func renameNoReplace(oldPath, newPath string) error {
	return renameThenRecheck(oldPath, newPath)
}
