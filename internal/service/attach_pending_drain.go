// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
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
// write. A failed write or alert leaves the row available for an honest retry.
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
	for _, row := range rows {
		dropped, removed, rowErr := s.drainAttachRow(ctx, store, row)
		if rowErr != nil {
			return result, rowErr
		}
		if removed {
			if dropped {
				result.Dropped++
			} else {
				result.Processed++
			}
		}
	}
	return result, nil
}

func (s *RegistrationService) drainAttachRow(ctx context.Context, store AttachPendingStore, row sqlite.AttachPending) (bool, bool, error) {
	payload, err := s.validateAttachRow(row)
	dropped := err != nil
	if dropped {
		_, err = s.Routing.State.RecordAlert(ctx, oplocal.Alert{Agent: "attach", Class: oplocal.AlertClassAttachPendingRejected,
			Detail: "Rejected an attach queue entry outside its board or lane.", Fix: "Run mtix agent register with your assigned id again."}, s.Clock())
	} else {
		err = s.applyAttach(ctx, row.Kind, payload)
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
