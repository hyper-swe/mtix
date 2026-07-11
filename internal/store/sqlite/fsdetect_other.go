// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package sqlite

// detectFS on platforms without a supported statfs classifier (e.g. Windows)
// cannot identify the filesystem, so it reports local (safe) and never blocks.
func detectFS(_ string) (string, fsClass, error) {
	return "", fsLocal, nil
}
