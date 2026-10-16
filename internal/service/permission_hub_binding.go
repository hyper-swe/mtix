// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// A binding precedes publication; only hub-wake observations attest completion.
// Identity is immutable even when a legacy physical store acquires a board ID.
type permissionHubBinding struct {
	Identity string `json:"identity"`
	BoardID  string `json:"board_id"`
}

func permissionBindingRecord(scope string) string {
	return strings.Replace(permissionHubRecord(scope), "hub-wake-", "wake-hub-binding-", 1)
}

func permissionBindingError() error {
	return fmt.Errorf("hub receipt identity unavailable or conflicting: %w", model.ErrOperatorStateUnreadable)
}

func (s *WakePermissions) readPermissionBinding(scope string) (permissionHubBinding, error) {
	var binding permissionHubBinding
	var raw json.RawMessage
	reports, err := s.State.ReadJSON(permissionBindingRecord(scope), &raw)
	if err != nil || len(reports) != 0 {
		return binding, permissionBindingError()
	}
	if raw == nil {
		return binding, nil
	}
	if err := json.Unmarshal(raw, &binding); err != nil || binding.Identity == "" {
		return binding, permissionBindingError()
	}
	return binding, validatePermissionBindingScope(scope, binding)
}

func validatePermissionBindingScope(scope string, binding permissionHubBinding) error {
	if !strings.HasPrefix(binding.Identity, "legacy-store:") && !strings.HasPrefix(binding.Identity, "board:") {
		return permissionBindingError()
	}
	if binding.BoardID != "" {
		id, parseErr := uuid.Parse(binding.BoardID)
		if parseErr != nil || id == uuid.Nil || id.String() != binding.BoardID {
			return permissionBindingError()
		}
	}
	if strings.HasPrefix(scope, "legacy-store:") && binding.Identity != scope {
		return permissionBindingError()
	}
	if strings.HasPrefix(binding.Identity, "board:") && binding.Identity != "board:"+binding.BoardID {
		return permissionBindingError()
	}
	if strings.HasPrefix(scope, "board:") && (binding.BoardID == "" || scope != "board:"+binding.BoardID) {
		return permissionBindingError()
	}
	return nil
}

func (s *WakePermissions) permissionObservation(identity string) (permissionHubObservation, error) {
	var observed permissionHubObservation
	reports, err := s.State.ReadJSON(permissionHubRecord(identity), &observed)
	if err != nil || len(reports) != 0 {
		return observed, permissionBindingError()
	}
	return observed, nil
}

func (s *WakePermissions) permissionReceiptIdentity(ctx context.Context, board PermissionBoard, epoch string) (string, error) {
	if board.ID == "" {
		if err := checkPermissionLegacyIdentity(board); err != nil {
			return "", err
		}
	}
	legacy := board
	legacy.ID = ""
	physical := permissionBoardScope(legacy)
	prior, err := s.readPermissionBinding(physical)
	if err != nil {
		return "", err
	}
	if board.ID == "" {
		if prior.BoardID != "" {
			return "", permissionBindingError()
		}
		if _, observationErr := s.permissionObservation(physical); observationErr != nil {
			return "", observationErr
		}
		if prior.Identity == "" {
			prior.Identity = physical
			err = s.State.WriteJSON(permissionBindingRecord(physical), prior)
		}
		return physical, err
	}
	return s.permissionCanonicalIdentity(ctx, board, prior, epoch)
}

func (s *WakePermissions) permissionCanonicalIdentity(ctx context.Context, board PermissionBoard, prior permissionHubBinding, epoch string) (string, error) {
	canonical := permissionBoardScope(board)
	if prior.BoardID != "" && prior.BoardID != board.ID {
		return "", permissionBindingError()
	}
	binding, err := s.readPermissionBinding(canonical)
	if err != nil {
		return "", err
	}
	if binding.Identity != "" {
		return s.validateCanonicalAssociation(binding, prior)
	}
	if _, observationErr := s.permissionObservation(canonical); observationErr != nil {
		return "", observationErr
	}
	legacy := board
	legacy.ID = ""
	physical := permissionBoardScope(legacy)
	adopt, err := s.permissionLegacyEvidence(ctx, prior, physical, epoch)
	if err != nil {
		return "", err
	}
	binding = permissionHubBinding{Identity: canonical, BoardID: board.ID}
	if adopt {
		if err := verifyPermissionIdentityFile(board); err != nil {
			return "", err
		}
		binding.Identity = physical
		if err := s.State.WriteJSON(permissionBindingRecord(physical), binding); err != nil {
			return "", err
		}
	}
	if err := s.State.WriteJSON(permissionBindingRecord(canonical), binding); err != nil {
		return "", err
	}
	return binding.Identity, nil
}

func (s *WakePermissions) validateCanonicalAssociation(binding, local permissionHubBinding) (string, error) {
	if local.Identity != "" && (local.Identity != binding.Identity || local.BoardID != binding.BoardID) {
		return "", permissionBindingError()
	}
	if strings.HasPrefix(binding.Identity, "legacy-store:") {
		bound, err := s.readPermissionBinding(binding.Identity)
		if err != nil || bound != binding {
			return "", permissionBindingError()
		}
	}
	return binding.Identity, nil
}

func verifyPermissionIdentityFile(board PermissionBoard) error {
	ids, err := ReadBoardIDs(filepath.Join(board.Checkout, ".mtix"))
	if err != nil || len(ids) != 1 || ids[0] != board.ID {
		return permissionBindingError()
	}
	return nil
}

func (s *WakePermissions) permissionLegacyEvidence(ctx context.Context, prior permissionHubBinding, physical, epoch string) (bool, error) {
	if prior.Identity != "" {
		return true, nil
	}
	observed, err := s.permissionObservation(physical)
	if err != nil || observed.Epoch != "" {
		return observed.Epoch != "", err
	}
	rows, err := s.Store.ListRegistrations(ctx, s.HostID, s.UID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		accepted, err := s.State.PermissionAlertAccepted(ctx, permissionHeldAlert(row.AgentID), s.permissionAlertOccurrence(physical, epoch, row.AgentID))
		if err != nil || accepted {
			return accepted, err
		}
	}
	return false, nil
}

func permissionHeldAlert(agent string) oplocal.Alert {
	return oplocal.Alert{Agent: agent, Class: "wake.unconfirmed",
		Detail: "Board became hub-connected; previously implicit registration is held",
		Fix:    "mtix agent allow " + agent}
}

func (s *WakePermissions) permissionAlertOccurrence(identity, epoch, agent string) oplocal.PermissionAlertOccurrence {
	return oplocal.PermissionAlertOccurrence{Slot: s.HostID + ":" + permissionHubRecord(identity) + ":" + agent, Key: epoch}
}

// A cached empty identity must not evaluate policy or publish under legacy scope
// once the checkout has a committed identity. Refresh the caller's whole board.
func checkPermissionLegacyIdentity(board PermissionBoard) error {
	_, err := ReadBoardIDs(filepath.Join(board.Checkout, ".mtix"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return permissionBindingError()
}
