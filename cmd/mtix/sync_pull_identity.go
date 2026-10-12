// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// recordPullObservation records only the outcome of a successful authenticated lookup.
func recordPullObservation(state *oplocal.State, checkout, boardID string, lookup func() (string, string, error)) error {
	prefix, hash, err := lookup()
	if err != nil {
		return fmt.Errorf("read hub identity: %w", err)
	}
	var hub oplocal.HubIdentity
	if prefix != "" && hash != "" {
		hub = oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: prefix, FirstEventHash: hash}
	}
	if err = state.ObserveBoard(checkout, boardID, hub); err != nil {
		return fmt.Errorf("cache hub identity: %w", err)
	}
	return nil
}

// cacheIdentityBestEffort runs cache and, on failure, writes a WARN to stderr
// without failing the caller (MTIX-112.17.2: data already applied by the pull
// is never rolled into a failure by identity caching).
func cacheIdentityBestEffort(stderr io.Writer, cache func() error) {
	if err := cache(); err != nil {
		warnSync(stderr, "WARN mtix sync pull: hub identity not cached", err)
	}
}

func cachePulledIdentity(ctx context.Context, pool *transport.Pool) error {
	return cachePulledIdentityFrom(ctx, pool.Inner())
}

func cachePulledIdentityFrom(ctx context.Context, pool identityQuerier) error {
	env, err := oplocal.FromProcess()
	if err != nil {
		return fmt.Errorf("resolve operator state: %w", err)
	}
	boardID, err := existingBoardIdentity()
	if err != nil {
		return err
	}
	prefix, err := app.configSvc.Get("prefix")
	if err != nil {
		return fmt.Errorf("read project prefix: %w", err)
	}
	return recordPullObservation(oplocal.New(env), filepath.Clean(filepath.Dir(app.mtixDir)), boardID, func() (string, string, error) { return queryHubIdentity(ctx, pool, prefix) })
}

func existingBoardIdentity() (string, error) {
	ids, err := service.ReadBoardIDs(app.mtixDir)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read committed board identity: %w", err)
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("multiple committed board identities: %w", model.ErrInvalidInput)
	}
	return ids[0], nil
}
