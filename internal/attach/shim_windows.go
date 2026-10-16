// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package attach

import "fmt"

// ValidateShimTarget refuses on Windows: the shim is a POSIX sh script and
// built-in wakes are unavailable on Windows (ADR-007 §2.10), so no shim is
// installed there.
func ValidateShimTarget(target string, _ int) (string, error) {
	return "", fmt.Errorf("%w: the attach shim is not supported on Windows (%s)", ErrShimTargetRefused, target)
}
