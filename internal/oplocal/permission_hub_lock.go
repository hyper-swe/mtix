// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"context"
	"errors"
)

// WithHubTransition serializes host-wide transition observation independently
// of permission publication and Alerts; read-only permission callbacks remain safe.
func (s *State) WithHubTransition(ctx context.Context, observe func() error) (err error) {
	if operationErr := ctx.Err(); operationErr != nil {
		return operationErr
	}
	dir, err := s.directory(true)
	if err != nil {
		return err
	}
	defer closeWith(dir, &err)
	lock, err := openNamedLock(dir, "hub-permissions.lock", true)
	if err != nil {
		return failure("hub transition lock unavailable", err)
	}
	if err = awaitAlertLock(ctx, lock); err != nil {
		return errors.Join(err, closeFile(lock))
	}
	defer func() { err = errors.Join(err, unlockAlert(lock)) }()
	return observe()
}
