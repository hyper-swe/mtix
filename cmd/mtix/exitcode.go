// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"

	"github.com/hyper-swe/mtix/internal/model"
)

// Structured exit codes per MTIX-26.8: storage failure classes are part
// of the CLI contract so scripts, agents, and the fault-injection suite
// can react without parsing error wording. Documented in USERMANUAL
// "Exit codes".
const (
	// exitCodeGeneric is any failure without a more specific class.
	exitCodeGeneric = 1

	// exitCodeDiskFull: a write or backup was refused (pre-flight) or
	// failed (ENOSPC) because the volume is out of space (NFR-2.8).
	exitCodeDiskFull = 3

	// exitCodeCorrupted: the database failed an integrity gate
	// (NFR-2.6a); recovery guidance was printed.
	exitCodeCorrupted = 4
)

// exitCodeForError maps an error to its contract exit code.
func exitCodeForError(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, model.ErrDiskFull):
		return exitCodeDiskFull
	case errors.Is(err, model.ErrCorrupted):
		return exitCodeCorrupted
	default:
		return exitCodeGeneric
	}
}
