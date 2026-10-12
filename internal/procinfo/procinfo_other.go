// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package procinfo

// StartTime reports ErrUnsupported: liveness on this platform is unknown, and
// callers fail safe (a live row is never replaced when liveness is unknown).
func StartTime(int) (string, error) { return "", ErrUnsupported }

// Info reports ErrUnsupported on this platform.
func Info(int) (Proc, error) { return Proc{}, ErrUnsupported }
