// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// ExportRequestState is constant-size, local export intent (MTIX-130.11).
// It is absent from the canonical board and its conflict baseline.
type ExportRequestState struct {
	Version   int    `json:"version"`
	Requested int64  `json:"requested"`
	Covered   int64  `json:"covered"`
	Oldest    string `json:"oldest"`
	Active    *int64 `json:"active"`
	Late      string `json:"late"`
}

func decodeExportRequest(raw string) (ExportRequestState, error) {
	var state ExportRequestState
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, fmt.Errorf("decode export request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return state, fmt.Errorf("trailing export request data: %w", errors.Join(model.ErrInvalidInput, err))
	}
	return state, validateExportRequest(state)
}

func validateExportRequest(state ExportRequestState) error {
	invalid := func() error { return fmt.Errorf("inconsistent export request state: %w", model.ErrInvalidInput) }
	if state.Version != 1 || state.Covered < 0 || state.Requested < state.Covered {
		return invalid()
	}
	if state.Requested == state.Covered {
		if state.Oldest != "" || state.Active != nil || state.Late != "" {
			return invalid()
		}
		return nil
	}
	oldest, err := time.Parse(time.RFC3339Nano, state.Oldest)
	if err != nil || !canonicalExportTime(state.Oldest, oldest) {
		return fmt.Errorf("invalid oldest export time: %w", errors.Join(model.ErrInvalidInput, err))
	}
	if state.Active == nil {
		if state.Late != "" {
			return invalid()
		}
		return nil
	}
	return validateActiveExportRequest(state, oldest)
}

func validateActiveExportRequest(state ExportRequestState, oldest time.Time) error {
	invalid := func() error { return fmt.Errorf("inconsistent active export request: %w", model.ErrInvalidInput) }
	if *state.Active <= state.Covered || *state.Active > state.Requested {
		return invalid()
	}
	if *state.Active == state.Requested {
		if state.Late != "" {
			return invalid()
		}
		return nil
	}
	late, err := time.Parse(time.RFC3339Nano, state.Late)
	if err != nil || !canonicalExportTime(state.Late, late) || late.Before(oldest) {
		return fmt.Errorf("invalid late export time: %w", errors.Join(model.ErrInvalidInput, err))
	}
	return nil
}

func canonicalExportTime(raw string, parsed time.Time) bool {
	return !parsed.IsZero() && parsed.Year() >= 1 && parsed.Year() <= 9999 && raw == parsed.UTC().Format(time.RFC3339Nano)
}

func earlierExportTime(existing string, requested time.Time) (string, error) {
	if existing != "" {
		old, err := time.Parse(time.RFC3339Nano, existing)
		if err != nil {
			return "", fmt.Errorf("parse existing export request time: %w", err)
		}
		if old.Before(requested) {
			return existing, nil
		}
	}
	return requested.UTC().Format(time.RFC3339Nano), nil
}
