// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

func awaitAlertLock(ctx context.Context, f *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("acquire alert lock: %w", err)
		}
		err := tryAlertLock(f)
		if err == nil {
			return nil
		}
		if !busyAlertLock(err) {
			return failure("alert lock failed", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("acquire alert lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (s *State) withAlerts(ctx context.Context, create bool, fn func(*os.File, string) error) (err error) {
	if err = ctx.Err(); err != nil {
		return fmt.Errorf("alert operation: %w", err)
	}
	dir, err := s.directory(create)
	if !create && missingState(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer closeWith(dir, &err)
	lock, err := openAlertLock(dir, create)
	if !create && errors.Is(err, os.ErrNotExist) {
		return absentAlertLock(dir)
	}
	if err != nil {
		return failure("alert lock unavailable", err)
	}
	if err = awaitAlertLock(ctx, lock); err != nil {
		return errors.Join(err, closeFile(lock))
	}
	defer func() { err = errors.Join(err, unlockAlert(lock)) }()
	host, err := s.hostID(dir, create)
	if err != nil {
		return err
	}
	return fn(dir, host)
}

func absentAlertLock(dir *os.File) error {
	for _, name := range []string{"alerts.jsonl", "alerts.1.jsonl", "alerts.2.jsonl", "alerts-pending.json"} {
		_, err := readData(dir, name)
		if !errors.Is(err, os.ErrNotExist) {
			return invalid("existing alert state has no transaction lock")
		}
	}
	return nil
}
