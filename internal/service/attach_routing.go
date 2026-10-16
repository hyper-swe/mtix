// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// AttachBoard is the queue owner's routing identity, supplied by the caller
// opening that board. Pending rows never choose their own destination.
type AttachBoard struct{ Key, Path string }

// AttachRouting supplies routing-only side writes for a registration. Callers
// can invoke the drain explicitly; command, glue and dispatcher wiring is
// deferred. These APIs never create a grant.
type AttachRouting struct {
	State    *oplocal.State
	Board    AttachBoard
	LanePath string
}

// AttachPendingStore is the service's narrow persistence contract.
type AttachPendingStore interface {
	EnqueueAttachPending(context.Context, sqlite.AttachPending) (int64, error)
	ListAttachPending(context.Context) ([]sqlite.AttachPending, error)
	DeleteAttachPending(context.Context, sqlite.AttachPending) (bool, error)
}

type pendingLane struct {
	AgentID  string `json:"agent_id"`
	Harness  string `json:"harness"`
	LanePath string `json:"lane_path"`
}

func (r *AttachRouting) location(harness string) oplocal.LaneLocation {
	return oplocal.LaneLocation{Harness: harness, BoardKey: r.Board.Key, Root: r.Board.Path, Path: r.LanePath}
}
func (r *AttachRouting) validate() error {
	if r.State == nil {
		return fmt.Errorf("operator routing state is missing: %w", model.ErrInvalidInput)
	}
	id, err := uuid.Parse(r.Board.Key)
	if err != nil || id == uuid.Nil || id.String() != r.Board.Key || !filepath.IsAbs(r.Board.Path) || filepath.Clean(r.Board.Path) != r.Board.Path {
		return fmt.Errorf("attach board identity or path is invalid: %w", model.ErrInvalidInput)
	}
	return nil
}
func (s *RegistrationService) prepareAttach(req RegisterSessionRequest) error {
	if s.Routing == nil {
		return nil
	}
	if err := s.Routing.validate(); err != nil {
		return err
	}
	return oplocal.ValidateLaneLocation(s.Routing.location(req.Harness))
}

func (s *RegistrationService) recordAttach(ctx context.Context, req RegisterSessionRequest) error {
	if s.Routing == nil {
		return nil
	}
	payload, err := json.Marshal(pendingLane{req.AgentID, req.Harness, s.Routing.LanePath})
	if err != nil {
		return fmt.Errorf("encode attach routing: %w", err)
	}
	for _, kind := range []string{"lane", "list"} {
		if writeErr := s.applyAttach(ctx, kind, pendingLane{req.AgentID, req.Harness, s.Routing.LanePath}); writeErr != nil {
			store, ok := s.Store.(AttachPendingStore)
			if !ok {
				return fmt.Errorf("registration committed but routing queue is unavailable: %w", errors.Join(writeErr, model.ErrInvalidInput))
			}
			row := sqlite.AttachPending{Kind: kind, BoardKey: s.Routing.Board.Key, Path: s.Routing.Board.Path, Payload: string(payload), QueuedAt: s.Clock().UTC().Format(time.RFC3339Nano)}
			if _, err = store.EnqueueAttachPending(ctx, row); err != nil {
				return fmt.Errorf("registration committed but routing side write was not queued: %w", errors.Join(writeErr, err))
			}
		}
	}
	return nil
}

func (s *RegistrationService) applyAttach(ctx context.Context, kind string, payload pendingLane) error {
	if kind == "list" {
		return s.Routing.State.ListBoard(ctx, s.Routing.Board.Key, s.Routing.Board.Path)
	}
	loc := oplocal.LaneLocation{Harness: payload.Harness, BoardKey: s.Routing.Board.Key, Root: s.Routing.Board.Path, Path: payload.LanePath}
	return s.Routing.State.LearnLane(ctx, loc, payload.AgentID)
}
