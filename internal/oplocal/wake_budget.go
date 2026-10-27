// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// ReadWakeBudget reads one atomic protected record without creating state.
func (s *State) ReadWakeBudget(key model.WakeRecipient) (WakeBudgetMirror, bool, error) {
	name, err := wakeBudgetName(key)
	if err != nil {
		return WakeBudgetMirror{}, false, err
	}
	mirror, present, err := s.readWakeBudgetRecord(name)
	if err == nil && present && mirror.Recipient != key {
		err = invalid("wake budget record belongs to another recipient")
	}
	return mirror, present, err
}

func (s *State) readWakeBudgetRecord(name string) (WakeBudgetMirror, bool, error) {
	var raw json.RawMessage
	reports, err := s.ReadJSON(name, &raw)
	if err != nil {
		return WakeBudgetMirror{}, false, err
	}
	if len(reports) != 0 {
		return WakeBudgetMirror{}, false, invalid("wake budget belongs to another host")
	}
	if len(raw) == 0 {
		return WakeBudgetMirror{}, false, nil
	}
	var mirror WakeBudgetMirror
	if decodeErr := json.Unmarshal(raw, &mirror); decodeErr != nil {
		return mirror, true, failure("wake budget data is invalid", decodeErr)
	}
	if validationErr := validateWakeBudgetMirror(mirror); validationErr != nil {
		return mirror, true, validationErr
	}
	expected, err := wakeBudgetName(mirror.Recipient)
	if err != nil || expected != name {
		return mirror, true, invalid("wake budget record key conflicts")
	}
	return mirror, true, nil
}

// UpdateWakeBudget serializes host-wide admission and acknowledgement publication.
// Checkpoint durably saves pending intent before a local claim can commit. An
// error after that checkpoint deliberately retains the conservative charge.
func (s *State) UpdateWakeBudget(ctx context.Context, key model.WakeRecipient,
	fn func(*WakeBudgetMirror, func() error) error,
) (err error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("start wake budget update: %w", contextErr)
	}
	name, err := wakeBudgetName(key)
	if err != nil {
		return err
	}
	if fn == nil {
		return invalid("wake budget update is required")
	}
	dir, err := s.directory(true)
	if err != nil {
		return err
	}
	defer closeWith(dir, &err)
	lock, err := openNamedLock(dir, "wake-budgets.lock", true)
	if err != nil {
		return failure("wake budget lock unavailable", err)
	}
	if err = awaitAlertLock(ctx, lock); err != nil {
		return errors.Join(err, closeFile(lock))
	}
	defer func() { err = errors.Join(err, unlockAlert(lock)) }()
	if _, err = s.hostID(dir, true); err != nil {
		return err
	}
	return s.updateWakeBudgetLocked(ctx, key, name, fn)
}

func (s *State) updateWakeBudgetLocked(ctx context.Context, key model.WakeRecipient, name string,
	fn func(*WakeBudgetMirror, func() error) error,
) error {
	mirror, present, err := s.ReadWakeBudget(key)
	if err != nil {
		return err
	}
	if !present {
		mirror = WakeBudgetMirror{Version: 1, Recipient: key, Sources: map[string]WakeBudgetSource{}, Credits: []WakeBudgetCredit{}}
	}
	save := func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("persist wake budget: %w", err)
		}
		if err := validateWakeBudgetMirror(mirror); err != nil {
			return err
		}
		return s.WriteJSON(name, mirror)
	}
	return fn(&mirror, save)
}
