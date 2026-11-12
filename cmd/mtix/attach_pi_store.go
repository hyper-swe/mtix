// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// piEnrolStore rechecks a long-lived wait's board before every poll capability,
// including writes after a successful claim (FR-20.4, MTIX-112.17.16.21).
type piEnrolStore struct {
	*sqlite.Store
	root string
}

// ListRegistrations refuses a poll while enrolment owns the board.
func (s *piEnrolStore) ListRegistrations(ctx context.Context, host, uid string) ([]sqlite.Registration, error) {
	if err := checkBoardEnrolGuard(s.root); err != nil {
		return nil, err
	}
	return s.Store.ListRegistrations(ctx, host, uid)
}

// InboxList rechecks enrolment before reading a registered agent's inbox.
func (s *piEnrolStore) InboxList(ctx context.Context, agent string) ([]sqlite.InboxEvent, error) {
	if err := checkBoardEnrolGuard(s.root); err != nil {
		return nil, err
	}
	return s.Store.InboxList(ctx, agent)
}

// ClaimWake rechecks enrolment before writing the claim ledger.
func (s *piEnrolStore) ClaimWake(ctx context.Context, key model.WakeKey, lease time.Duration) (model.WakeAttempt, bool, error) {
	if err := checkBoardEnrolGuard(s.root); err != nil {
		return model.WakeAttempt{}, false, err
	}
	return s.Store.ClaimWake(ctx, key, lease)
}

// RecordWakeResult rechecks enrolment before writing a terminal result.
func (s *piEnrolStore) RecordWakeResult(ctx context.Context, attempt model.WakeAttempt, result model.WakeResult) error {
	if err := checkBoardEnrolGuard(s.root); err != nil {
		return err
	}
	return s.Store.RecordWakeResult(ctx, attempt, result)
}

// UpgradeWakeConfirmed rechecks enrolment before settling a reclaimed claim as confirmed.
func (s *piEnrolStore) UpgradeWakeConfirmed(ctx context.Context, key model.WakeKey) error {
	if err := checkBoardEnrolGuard(s.root); err != nil {
		return err
	}
	return s.Store.UpgradeWakeConfirmed(ctx, key)
}

// InboxEventAcked rechecks enrolment before reading the acknowledgement evidence.
func (s *piEnrolStore) InboxEventAcked(ctx context.Context, agent string, seq int64) (bool, error) {
	if err := checkBoardEnrolGuard(s.root); err != nil {
		return false, err
	}
	return s.Store.InboxEventAcked(ctx, agent, seq)
}
