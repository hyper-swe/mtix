// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// DrainAttachPendingResult counts only rows actually removed by this drain.
type DrainAttachPendingResult struct{ Processed, Dropped int }

// DrainAttachPending validates untrusted routing requests before any operator
// write. Transient write or alert failures keep retry rows without blocking later
// requests. Permanent routing conflicts are rejected with an alert.
func (s *RegistrationService) DrainAttachPending(ctx context.Context, board AttachBoard) (DrainAttachPendingResult, error) {
	var result DrainAttachPendingResult
	if s.Routing == nil || board != s.Routing.Board {
		return result, fmt.Errorf("drain board does not own this queue: %w", model.ErrInvalidInput)
	}
	if err := s.Routing.validate(); err != nil {
		return result, err
	}
	store, ok := s.Store.(AttachPendingStore)
	if !ok {
		return result, fmt.Errorf("attach queue is unavailable: %w", model.ErrInvalidInput)
	}
	rows, err := store.ListAttachPending(ctx)
	if err != nil {
		return result, err
	}
	var drainErr error
	for _, row := range rows {
		if contextErr := ctx.Err(); contextErr != nil {
			return result, fmt.Errorf("drain attach pending: %w", errors.Join(drainErr, contextErr))
		}
		dropped, removed, rowErr := s.drainAttachRow(ctx, store, row)
		drainErr = errors.Join(drainErr, rowErr)
		if removed {
			if dropped {
				result.Dropped++
			} else {
				result.Processed++
			}
		}
	}
	return result, drainErr
}

func (s *RegistrationService) drainAttachRow(ctx context.Context, store AttachPendingStore, row sqlite.AttachPending) (bool, bool, error) {
	payload, validationErr := s.validateAttachRow(row)
	err := validationErr
	if err == nil {
		err = s.applyAttach(ctx, row.Kind, payload)
	}
	dropped := validationErr != nil || errors.Is(err, model.ErrConflict)
	if dropped {
		_, err = s.Routing.State.RecordAlert(ctx, oplocal.Alert{Agent: "attach", Class: oplocal.AlertClassAttachPendingRejected,
			Detail: "Rejected an attach queue entry with invalid or conflicting board or lane routing.", Fix: "Verify the assigned id and board; ask your operator to reconcile conflicting listed-board identities before registering again."}, s.Clock())
	}
	if err != nil {
		return dropped, false, fmt.Errorf("attach pending row %d remains queued: %w", row.ID, err)
	}
	removed, err := store.DeleteAttachPending(ctx, row)
	return dropped, removed, err
}

func (s *RegistrationService) validateAttachRow(row sqlite.AttachPending) (pendingLane, error) {
	var payload pendingLane
	if row.BoardKey != s.Routing.Board.Key || row.Path != s.Routing.Board.Path || (row.Kind != "lane" && row.Kind != "list") || len(row.Payload) > 4096 {
		return payload, fmt.Errorf("attach queue owner or kind is invalid: %w", model.ErrInvalidInput)
	}
	decoder := json.NewDecoder(strings.NewReader(row.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return payload, fmt.Errorf("decode attach routing: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return payload, fmt.Errorf("attach payload has trailing data: %w", model.ErrInvalidInput)
	}
	if payload.AgentID == "" {
		return payload, fmt.Errorf("attach payload has no agent id: %w", model.ErrInvalidInput)
	}
	if err := validateAgentID(payload.AgentID); err != nil {
		return payload, err
	}
	loc := oplocal.LaneLocation{Harness: payload.Harness, BoardKey: row.BoardKey, Root: row.Path, Path: payload.LanePath}
	return payload, oplocal.ValidateLaneLocation(loc)
}
