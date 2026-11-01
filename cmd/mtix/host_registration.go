// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// currentRegistrationRouting binds explicit registration to this local store instance.
func currentRegistrationRouting() (*service.AttachRouting, error) {
	if app.store == nil || app.mtixDir == "" {
		return nil, nil
	}
	instance, err := app.store.StoreInstanceID(context.Background())
	if err != nil {
		return nil, err
	}
	if instance == "" {
		return nil, nil
	} // legacy stores need explicit setup before routing activation
	key := instance
	ids, err := service.ReadBoardIDs(app.mtixDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(ids) > 1 {
		return nil, fmt.Errorf("board identity conflict: %w", model.ErrConflict)
	}
	if len(ids) == 1 {
		key = ids[0]
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(app.mtixDir))
	if err != nil {
		return nil, err
	}
	lane, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	lane, err = filepath.EvalSymlinks(lane)
	if err != nil {
		return nil, err
	}
	state, err := operatorState()
	if err != nil {
		return nil, err
	}
	return &service.AttachRouting{State: state, Board: service.AttachBoard{Key: key, Path: root}, LanePath: lane, StoreInstanceID: instance}, nil
}

// listedHostBoard verifies the physical directories before opening an existing store.
func listedHostBoard(row oplocal.ListedBoard) error {
	physical, err := filepath.EvalSymlinks(row.Path)
	if err != nil {
		return err
	}
	if physical != row.Path {
		return fmt.Errorf("listed checkout path changed: %w", model.ErrConflict)
	}
	if err := oplocal.ValidateLaneLocation(oplocal.LaneLocation{Harness: "dispatcher", Root: row.Path, Path: row.Path}); err != nil {
		return err
	}
	for _, name := range []string{".mtix", ".mtix/data", ".mtix/data/mtix.db"} {
		info, err := os.Lstat(filepath.Join(row.Path, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		if name == ".mtix/data/mtix.db" {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("host board store must be real: %w", model.ErrInvalidInput)
			}
		} else if !info.IsDir() {
			return fmt.Errorf("host board directory must be real: %w", model.ErrInvalidInput)
		}
	}
	return nil
}
