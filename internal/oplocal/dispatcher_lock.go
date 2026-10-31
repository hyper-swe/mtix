// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/hyper-swe/mtix/internal/model"
)

// DispatcherLock holds the one-per-operator process lock, released by process exit.
type DispatcherLock struct{ file *os.File }

// AcquireDispatcher obtains a protected nonblocking host lock independent of any board.
func (s *State) AcquireDispatcher(ctx context.Context) (result *DispatcherLock, retErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := s.directory(true)
	if err != nil {
		return nil, err
	}
	defer closeWith(dir, &retErr)
	file, err := openNamedLock(dir, "dispatcher.lock", true)
	if err != nil {
		return nil, err
	}
	if err := tryAlertLock(file); err != nil {
		closeErr := closeFile(file)
		if busyAlertLock(err) {
			return nil, fmt.Errorf("host dispatcher already running: %w", errors.Join(model.ErrConflict, closeErr))
		}
		return nil, errors.Join(err, closeErr)
	}
	return &DispatcherLock{file: file}, nil
}

// Close releases this process's lock; repeat calls are harmless.
func (l *DispatcherLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return unlockAlert(file)
}
