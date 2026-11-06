// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/hyper-swe/mtix/internal/model"
	"time"
)

var errAnnotationAlreadyPresent = errors.New("annotation already present")

// AppendAnnotationOnce checks the stable identifier and appends in the same
// write transaction. An existing identical annotation produces no sync event.
func (s *Store) AppendAnnotationOnce(ctx context.Context, nodeID string, annotation model.Annotation) error {
	if annotation.ID == "" {
		return fmt.Errorf("annotation identifier required: %w", model.ErrInvalidInput)
	}
	err := s.mutateAnnotations(ctx, nodeID, func(current []model.Annotation) ([]model.Annotation, error) {
		for _, old := range current {
			if old.ID != annotation.ID {
				continue
			}
			if old.Author != annotation.Author || old.Text != annotation.Text || old.Addressee != annotation.Addressee {
				return nil, fmt.Errorf("annotation identifier conflicts: %w", model.ErrConflict)
			}
			validTime, err := s.annotationReplayTime(ctx, nodeID, old, annotation.CreatedAt)
			if err != nil {
				return nil, err
			}
			if !validTime {
				return nil, fmt.Errorf("annotation timestamp conflicts: %w", model.ErrConflict)
			}
			return nil, errAnnotationAlreadyPresent
		}
		result := append([]model.Annotation{}, current...)
		return append(result, annotation), nil
	}, annotation.ID, annotation.Hop)
	if errors.Is(err, errAnnotationAlreadyPresent) {
		return nil
	}
	return err
}

// annotationReplayTime accepts the original private time or the ordinary
// event's reconstructed wire time, without changing a peer's resolution.
// The existing write transaction serializes all mutations of this event id.
func (s *Store) annotationReplayTime(ctx context.Context, node string, old model.Annotation, original time.Time) (bool, error) {
	if old.CreatedAt.Equal(original) {
		return true, nil
	}
	var stamp int64
	err := s.readDB.QueryRowContext(ctx, `SELECT wall_clock_ts FROM sync_events WHERE event_id = ? AND node_id = ? AND op_type = ?`, old.ID, node, model.OpComment).Scan(&stamp)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("annotation replay event is missing: %w", model.ErrConflict)
	}
	if err != nil {
		return false, fmt.Errorf("read annotation replay time: %w", err)
	}
	return old.CreatedAt.Equal(eventTime(stamp, original)), nil
}
