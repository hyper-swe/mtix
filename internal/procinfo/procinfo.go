// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package procinfo reads a process's start time from the operating system
// without running external programs. A pid plus its start time identifies one
// process for the lifetime of the machine, so a reused pid never reads as the
// session that registered (ADR-007 §3.4, MTIX-112.17.4).
package procinfo

import "errors"

var (
	// ErrNoProcess means the pid does not name a running process.
	ErrNoProcess = errors.New("no such process")
	// ErrUnsupported means this platform cannot report a process start time.
	// Callers must treat liveness as unknown and fail safe.
	ErrUnsupported = errors.New("process start time unsupported on this platform")
)
