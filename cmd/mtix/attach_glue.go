// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// productionGluePredicate evaluates the glue activation predicate for the
// process's working directory against operator-local state (MTIX-112.19.8,
// ADR-007 A5). It reads only: the registry read creates no state and the store
// identity comes from a read-only open. Any failure declines.
func productionGluePredicate(ctx context.Context) attach.GlueDecision {
	cwd, err := os.Getwd()
	if err != nil {
		return attach.GlueDecision{Reason: attach.ReasonNoCheckout, Line: attach.NotAttachedLine}
	}
	return attach.EvaluateGlue(ctx, productionPredicateDeps(), cwd)
}

func productionPredicateDeps() attach.PredicateDeps {
	return attach.PredicateDeps{
		Boards: func(ctx context.Context) (oplocal.DispatchBoards, error) {
			st, err := operatorState()
			if err != nil {
				return oplocal.DispatchBoards{}, err
			}
			return st.DispatchBoards(ctx)
		},
		StoreInstanceID: sqlite.ReadStoreInstanceIDFile,
	}
}

// productionGlueDrain drains the pending attach queue of the board the glue
// predicate matched, through the same validating drain the host dispatcher
// runs (service.DrainAttachPending, MTIX-112.17.13) over the matched root's own store: a
// listed linked worktree drains its own queue, not the main checkout's; there is no second
// implementation. Rows with a foreign board key or path are dropped with an
// attach.pending-rejected alert. Before opening the store, it honors the
// enrolment guard (FR-20.4, MTIX-112.17.16.21). It runs no import and no hook.
func productionGlueDrain(ctx context.Context, d attach.GlueDecision) (retErr error) {
	if err := service.CheckEnrolGuard(filepath.Join(d.BoardRoot, ".mtix"), false); err != nil {
		return fmt.Errorf("drain attach queue: %w", err)
	}
	st, err := operatorState()
	if err != nil {
		return fmt.Errorf("drain attach queue: %w", err)
	}
	store, err := sqlite.New(filepath.Join(d.BoardRoot, ".mtix", "data"), slog.Default())
	if err != nil {
		return fmt.Errorf("open attached board store: %w", err)
	}
	defer func() {
		if closeErr := store.Close(); retErr == nil && closeErr != nil {
			retErr = fmt.Errorf("close attached board store: %w", closeErr)
		}
	}()
	instance, err := store.StoreInstanceID(ctx)
	if err != nil {
		return fmt.Errorf("read attached board identity: %w", err)
	}
	if instance == "" || d.BoardKey == "" {
		return nil // a legacy store has no routing identity: nothing to drain
	}
	routing := &service.AttachRouting{State: st, Board: service.AttachBoard{Key: d.BoardKey, Path: d.BoardRoot}, LanePath: d.BoardRoot, StoreInstanceID: instance}
	reg := service.NewRegistrationService(service.RegistrationDeps{Store: store, Clock: func() time.Time { return time.Now().UTC() }, Routing: routing})
	if _, err := reg.DrainAttachPending(ctx, routing.Board); err != nil {
		return fmt.Errorf("drain attach queue: %w", err)
	}
	return nil
}
