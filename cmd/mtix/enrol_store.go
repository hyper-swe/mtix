// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// boardEnrolGuardError distinguishes enrolment recovery from an operator-state
// or adapter failure while retaining the original error chain.
type boardEnrolGuardError struct{ cause error }

// Error reports the enrolment guard's cause with board context.
func (e *boardEnrolGuardError) Error() string { return "board enrolment guard: " + e.cause.Error() }

// Unwrap preserves the original enrolment sentinel and filesystem error.
func (e *boardEnrolGuardError) Unwrap() error { return e.cause }

func checkBoardEnrolGuard(root string) error {
	if err := service.CheckEnrolGuard(filepath.Join(root, ".mtix"), false); err != nil {
		return &boardEnrolGuardError{cause: err}
	}
	return nil
}

// openEnrolGuardedStore is the existing-store opener shared by initApp-bypassing
// writers (FR-20.4, MTIX-112.17.16.21). This checks, rather than holds, enrolment.
// A later move may still overlap the operation; an explicit database filename
// prevents a missing data directory from becoming a regular SQLite file.
func openEnrolGuardedStore(root string, logger *slog.Logger) (*sqlite.Store, error) {
	if err := checkBoardEnrolGuard(root); err != nil {
		return nil, err
	}
	data := filepath.Join(root, ".mtix", "data")
	info, err := os.Stat(data)
	if err != nil {
		return nil, fmt.Errorf("the board at %s has no data directory: %w", filepath.Join(root, ".mtix"), err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("the board data path is not a directory: %w", model.ErrInvalidInput)
	}
	return sqlite.New(filepath.Join(data, "mtix.db"), logger)
}
