// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// PermissionAlertOccurrence identifies the current durable transition for one slot.
// Callers serialize transitions and never retry obsolete keys after a newer key.
type PermissionAlertOccurrence struct {
	Slot string `json:"slot"`
	Key  string `json:"key"`
}
type permissionAlertPending struct {
	PermissionAlertOccurrence
	InputHash string `json:"input_hash"`
}
type permissionAlertReceipt struct {
	Occurrence permissionAlertPending `json:"occurrence"`
	Row        *Alert                 `json:"row"`
}
type alertAcceptance struct {
	row      Alert
	segments [3]alertSegment
	occ      *PermissionAlertOccurrence
	now      time.Time
}

// RecordPermissionAlert uses the real counted Alerts transaction, with retry identity.
func (s *State) RecordPermissionAlert(ctx context.Context, input Alert, now time.Time, occ PermissionAlertOccurrence) (Alert, error) {
	if err := ValidateAlertInput(input); err != nil {
		return Alert{}, err
	}
	if err := validatePermissionAlertOccurrence(occ); err != nil {
		return Alert{}, err
	}
	if now.IsZero() {
		return Alert{}, fmt.Errorf("permission alert time is invalid: %w", model.ErrInvalidInput)
	}
	var row Alert
	err := s.withAlerts(ctx, true, func(dir *os.File, host string) error {
		var err error
		row, err = s.recordAlertLocked(dir, host, input, now.UTC(), &occ)
		return err
	})
	return row, err
}
func (s *State) acceptCountedAlert(dir *os.File, host string, a alertAcceptance) (Alert, error) {
	row := a.row
	if err := validateProposedAlert(row, a.segments); err != nil {
		return row, err
	}
	pending, err := prepareAlertProjection(row, a.segments)
	if err != nil {
		return row, err
	}
	if a.occ != nil {
		hash, hashErr := checkedPermissionAlertInputHash(row)
		if hashErr != nil {
			return row, hashErr
		}
		pending.PermissionOccurrence = &permissionAlertPending{PermissionAlertOccurrence: *a.occ, InputHash: hash}
	}
	if operationErr := s.writeAlertRecord(dir, "alerts-pending.json", host, pending); operationErr != nil {
		return row, operationErr
	}
	if operationErr := s.finishAlert(dir, host, pending, a.segments); operationErr != nil {
		return row, operationErr
	}
	// Preserve ordinary Alerts semantics: accepted occurrences are not retried for cleanup failures.
	if err = s.pruneAlertRates(dir, host, a.now); err != nil {
		slog.Warn("operator alert cleanup failed", "error", err)
	}
	return row, nil
}
func checkedPermissionAlertInputHash(row Alert) (string, error) {
	return permissionRecoveryHash(struct{ Agent, Class, Detail, Fix string }{row.Agent, row.Class, row.Detail, row.Fix})
}

func permissionAlertReceiptName(slot string) string {
	sum := sha256.Sum256([]byte(slot))
	return "permission-alert-" + hex.EncodeToString(sum[:16]) + ".json"
}
func validatePermissionAlertOccurrence(occ PermissionAlertOccurrence) error {
	if !permissionText(occ.Slot) || !permissionText(occ.Key) {
		return invalid("permission alert occurrence is invalid")
	}
	return nil
}
func validatePermissionAlertPending(p alertPending) error {
	if p.PermissionOccurrence == nil {
		return nil
	}
	if err := validatePermissionAlertOccurrence(p.PermissionOccurrence.PermissionAlertOccurrence); err != nil {
		return err
	}
	if p.Row == nil {
		return invalid("permission alert input is missing")
	}
	hash, err := checkedPermissionAlertInputHash(*p.Row)
	if err != nil {
		return err
	}
	if p.PermissionOccurrence.InputHash != hash {
		return invalid("permission alert input conflicts")
	}
	return nil
}
func readPermissionAlertReceipt(dir *os.File, host string, occ PermissionAlertOccurrence, input Alert) (Alert, bool, error) {
	var receipt permissionAlertReceipt
	present, err := readAlertRecord(dir, permissionAlertReceiptName(occ.Slot), host, &receipt)
	if err != nil || !present {
		return Alert{}, false, err
	}
	if operationErr := validatePermissionAlertReceipt(receipt, host); operationErr != nil {
		return Alert{}, false, operationErr
	}
	if receipt.Occurrence.Slot != occ.Slot {
		return Alert{}, false, invalid("permission alert slot conflicts")
	}
	if receipt.Occurrence.Key != occ.Key {
		return Alert{}, false, nil
	}
	hash, err := checkedPermissionAlertInputHash(input)
	if err != nil {
		return Alert{}, false, err
	}
	if receipt.Occurrence.InputHash != hash {
		return Alert{}, false, invalid("permission alert request conflicts")
	}
	return *receipt.Row, true, nil
}
func validatePermissionAlertReceipt(receipt permissionAlertReceipt, host string) error {
	if receipt.Row == nil {
		return invalid("permission alert receipt is empty")
	}
	if err := validateStoredAlert(*receipt.Row, host); err != nil {
		return err
	}
	return validatePermissionAlertPending(alertPending{Row: receipt.Row, PermissionOccurrence: &receipt.Occurrence})
}
func (s *State) finishPermissionAlertReceipt(dir *os.File, host string, p alertPending) error {
	if p.PermissionOccurrence == nil {
		return nil
	}
	if err := validatePermissionAlertPending(p); err != nil {
		return err
	}
	receipt := permissionAlertReceipt{Occurrence: *p.PermissionOccurrence, Row: p.Row}
	return s.writeAlertRecord(dir, permissionAlertReceiptName(receipt.Occurrence.Slot), host, receipt)
}

func validatePermissionAlertReceiptPreimage(dir *os.File, host string, p alertPending) error {
	if p.PermissionOccurrence == nil {
		return nil
	}
	var receipt permissionAlertReceipt
	present, err := readAlertRecord(dir, permissionAlertReceiptName(p.PermissionOccurrence.Slot), host, &receipt)
	if err != nil || !present {
		return err
	}
	if operationErr := validatePermissionAlertReceipt(receipt, host); operationErr != nil {
		return operationErr
	}
	if receipt.Occurrence.Slot != p.PermissionOccurrence.Slot {
		return invalid("permission alert receipt slot conflicts")
	}
	if receipt.Occurrence.Key != p.PermissionOccurrence.Key {
		return nil
	}
	expected, err := json.Marshal(permissionAlertReceipt{Occurrence: *p.PermissionOccurrence, Row: p.Row})
	if err != nil {
		return failure("permission alert expected receipt", err)
	}
	actual, err := json.Marshal(receipt)
	if err != nil {
		return failure("permission alert actual receipt", err)
	}
	if string(expected) != string(actual) {
		return invalid("permission alert receipt conflicts")
	}
	return nil
}
