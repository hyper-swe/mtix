// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// RaisePermissionTransition uses the actual counted Alerts transaction with a
// durable per-board-agent occurrence receipt; replay does not increase Count.
func (a *Alerts) RaisePermissionTransition(ctx context.Context, input AlertRequest, occurrence oplocal.PermissionAlertOccurrence) (oplocal.Alert, error) {
	if err := validateAgentID(input.Agent); err != nil {
		return oplocal.Alert{}, err
	}
	row, err := a.state.RecordPermissionAlert(ctx, oplocal.Alert{
		Agent: input.Agent, Class: input.Class, Detail: input.Detail, Fix: input.Fix,
	}, a.clock(), occurrence)
	if err != nil {
		return row, fmt.Errorf("raise permission transition alert: %w", err)
	}
	return row, nil
}

type permissionHubObservation struct {
	Epoch string `json:"epoch"`
}

// ObserveHub rereads the actual local mark inside the host transition lock;
// callers cannot replay a cached older epoch after a newer transition completes.
func (s *WakePermissions) ObserveHub(ctx context.Context, board PermissionBoard) error {
	return s.State.WithHubTransition(ctx, func() error {
		mark, err := s.Store.HubMark(ctx)
		if err != nil || !mark.Set {
			return err
		}
		identity, err := s.permissionReceiptIdentity(ctx, board, mark.Epoch)
		if err != nil {
			return err
		}
		key := permissionHubRecord(identity)
		var observed permissionHubObservation
		reports, err := s.State.ReadJSON(key, &observed)
		if err != nil || len(reports) != 0 {
			return fmt.Errorf("hub transition observation unavailable: %w", model.ErrOperatorStateUnreadable)
		}
		if observed.Epoch == mark.Epoch {
			return nil
		}
		if err := s.alertHeldRegistrations(ctx, board, identity, mark.Epoch); err != nil {
			return err
		}
		return s.State.WriteJSON(key, permissionHubObservation{Epoch: mark.Epoch})
	})
}

func permissionBoardScope(board PermissionBoard) string {
	if board.ID != "" {
		return "board:" + board.ID
	}
	// Legacy boards have no shared board identity until allow/deny assigns it.
	// Resolve aliases of the same physical store instead of treating them as hosts.
	if root, err := filepath.EvalSymlinks(board.Checkout); err == nil {
		return "legacy-store:" + root
	}
	return "legacy-store:" + filepath.Clean(board.Checkout)
}

func permissionHubRecord(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return "hub-wake-" + hex.EncodeToString(sum[:16])
}

func (s *WakePermissions) alertHeldRegistrations(ctx context.Context, board PermissionBoard, identity, epoch string) error {
	rows, err := s.Store.ListRegistrations(ctx, s.HostID, s.UID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		ok, reason, err := s.Wakeable(ctx, board, row.AgentID)
		if err != nil {
			return err
		}
		if ok || reason == "denied" {
			continue
		}
		input := permissionHeldAlert(row.AgentID)
		_, err = NewAlerts(s.State, s.Clock).RaisePermissionTransition(ctx, AlertRequest{
			Agent: input.Agent, Class: input.Class, Detail: input.Detail, Fix: input.Fix,
		}, s.permissionAlertOccurrence(identity, epoch, row.AgentID))
		if err != nil {
			return err
		}
	}
	return nil
}
