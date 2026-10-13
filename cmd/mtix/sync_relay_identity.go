// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"path/filepath"
)

// cacheVerifiedRelay records only the identity returned by successful relay attach.
func cacheVerifiedRelay(dir, relayID string) error {
	env, err := oplocal.FromProcess()
	if err != nil {
		return fmt.Errorf("resolve operator state: %w", err)
	}
	boardID, err := existingBoardIdentity()
	if err != nil {
		return err
	}
	path, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve verified relay: %w", err)
	}
	if err = oplocal.New(env).ObserveRelay(filepath.Clean(filepath.Dir(app.mtixDir)), boardID, path, relayID); err != nil {
		return fmt.Errorf("cache verified relay: %w", err)
	}
	return nil
}
