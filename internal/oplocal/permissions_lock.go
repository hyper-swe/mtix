// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Permission transactions have a distinct protected lock: publication hooks
// may raise real operator alerts while this mutex is held.
func (s *State) withPermissions(ctx context.Context, fn func(*os.File, string) error) (err error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("permission operation: %w", contextErr)
	}
	dir, err := s.directory(true)
	if err != nil {
		return err
	}
	defer closeWith(dir, &err)
	lock, err := openNamedLock(dir, "permissions.lock", true)
	if err != nil {
		return failure("permission lock unavailable", err)
	}
	if err = awaitAlertLock(ctx, lock); err != nil {
		return errors.Join(err, closeFile(lock))
	}
	defer func() { err = errors.Join(err, unlockAlert(lock)) }()
	host, err := s.hostID(dir, true)
	if err != nil {
		return err
	}
	return fn(dir, host)
}
