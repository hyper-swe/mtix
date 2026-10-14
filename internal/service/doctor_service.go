// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/redact"
)

// ErrDoctorChecksFailed means the complete doctor report contains a FAIL (ADR-007 §7).
var ErrDoctorChecksFailed = errors.New("doctor checks failed")

// DoctorStatus is a diagnostic outcome, independent of command execution errors.
type DoctorStatus string

// Doctor outcomes follow ADR-007 §7; WARN and INFO do not fail the command.
const (
	DoctorPass DoctorStatus = "pass"
	DoctorWarn DoctorStatus = "warn"
	DoctorFail DoctorStatus = "fail"
	DoctorInfo DoctorStatus = "info"
)

// DoctorResult is the four-field public doctor row, including empty detail and fix.
type DoctorResult struct {
	ID     string       `json:"id"`
	Status DoctorStatus `json:"status"`
	Detail string       `json:"detail"`
	Fix    string       `json:"fix"`
}

// DoctorCheck registers one feature's diagnostic and optional typed repair.
// Run receives the caller's context. Its result ID is supplied by the registration.
type DoctorCheck struct {
	ID    string
	Run   func(context.Context) DoctorResult
	Fixer *DoctorFixer
}

// DoctorOptions injects platform, existing sync collection, and final output scrubbing.
// CollectSync is nil when sync is unconfigured and is called once per Run otherwise.
type DoctorOptions struct {
	Platform    string
	CollectSync func(context.Context) ([]DoctorResult, error)
	Scrub       func(string) string
}

// DoctorRunOptions selects explicitly allowed repairs; the default is diagnostic only.
type DoctorRunOptions struct {
	Fix bool
}

// DoctorService owns an ordered registry. Register checks before calling Run;
// registry mutation and concurrent Run calls require external synchronization.
type DoctorService struct {
	checks  []DoctorCheck
	collect func(context.Context) ([]DoctorResult, error)
	scrub   func(string) string
}

// NewDoctorService wires an independent registry without process-global state.
// Windows gets the existing platform information seed; future checks register separately.
func NewDoctorService(opts DoctorOptions) *DoctorService {
	s := &DoctorService{collect: opts.CollectSync, scrub: opts.Scrub}
	if s.scrub == nil {
		s.scrub = redact.DSN
	}
	if opts.Platform == "windows" {
		s.checks = append(s.checks, DoctorCheck{ID: "platform.windows", Run: func(context.Context) DoctorResult {
			return DoctorResult{Status: DoctorInfo, Detail: "Built-in wakes unavailable on Windows; use explicit inbox reads and acknowledgements."}
		}})
	}
	return s
}

// Register adds a check in execution order (ADR-007 §7). Empty IDs or callbacks
// are invalid; duplicate IDs are refused rather than replacing another feature.
func (s *DoctorService) Register(check DoctorCheck) error {
	if strings.TrimSpace(check.ID) == "" || check.Run == nil {
		return fmt.Errorf("register doctor check %q: %w", s.scrub(check.ID), model.ErrInvalidInput)
	}
	for _, existing := range s.checks {
		if existing.ID == check.ID {
			return fmt.Errorf("register doctor check %q: %w", s.scrub(check.ID), model.ErrAlreadyExists)
		}
	}
	s.checks = append(s.checks, check)
	return nil
}

// Run collects configured sync checks once and registered checks in order.
// It returns a non-nil array, final redacted fields, and ErrDoctorChecksFailed
// for FAIL rows. Cancellation and collection errors retain their error chains.
func (s *DoctorService) Run(ctx context.Context, opts DoctorRunOptions) (rows []DoctorResult, err error) {
	rows = make([]DoctorResult, 0)
	defer func() {
		rows = s.scrubRows(rows)
		err = s.RedactError(err)
	}()
	if err = ctx.Err(); err != nil {
		return rows, fmt.Errorf("run doctor: %w", err)
	}
	if s.collect != nil {
		var collected []DoctorResult
		collected, err = s.collect(ctx)
		rows = append(rows, collected...)
		if err != nil {
			return rows, fmt.Errorf("collect sync checks: %w", err)
		}
	}
	for _, check := range s.checks {
		if err = ctx.Err(); err != nil {
			return rows, fmt.Errorf("run doctor check: %w", err)
		}
		rows = append(rows, s.runCheck(ctx, check, opts))
	}
	if err = ctx.Err(); err != nil {
		return rows, fmt.Errorf("finish doctor: %w", err)
	}
	return rows, validateDoctorRows(rows)
}

func validateDoctorRows(rows []DoctorResult) error {
	failed := false
	ids := make(map[string]bool)
	for _, row := range rows {
		if strings.TrimSpace(row.ID) == "" || ids[row.ID] {
			return fmt.Errorf("doctor result ID %q: %w", row.ID, model.ErrInvalidInput)
		}
		ids[row.ID] = true
		switch row.Status {
		case DoctorFail:
			failed = true
		case DoctorPass, DoctorWarn, DoctorInfo:
		default:
			return fmt.Errorf("doctor result status %q: %w", row.Status, model.ErrInvalidInput)
		}
	}
	if failed {
		return ErrDoctorChecksFailed
	}
	return nil
}

func (s *DoctorService) scrubRows(rows []DoctorResult) []DoctorResult {
	for i := range rows {
		rows[i].ID = s.scrub(rows[i].ID)
		rows[i].Status = DoctorStatus(s.scrub(string(rows[i].Status)))
		rows[i].Detail = s.scrub(rows[i].Detail)
		rows[i].Fix = s.scrub(rows[i].Fix)
	}
	return rows
}

// RedactError masks an adapter diagnostic while preserving errors.Is/As (FR-18.17).
// It accepts nil so callers can use it on every return path, including write errors.
func (s *DoctorService) RedactError(err error) error {
	if err == nil {
		return nil
	}
	return &doctorOutputError{cause: err, text: s.scrub(err.Error())}
}

type doctorOutputError struct {
	cause error
	text  string
}

func (e *doctorOutputError) Error() string { return e.text }
func (e *doctorOutputError) Unwrap() error { return e.cause }
