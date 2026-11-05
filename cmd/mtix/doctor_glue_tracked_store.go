// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// trackedStoreCheck registers the glue.tracked-store row for the directory
// doctor runs in, only when the glue activation predicate declined because the
// store is committed or the git index could not be read (MTIX-112.19.8, ADR-007
// A5). The glue stays off there (fail closed) until the row is green. The row
// is for boards listed without a recorded instance id; an identified store
// needs no index test.
func trackedStoreCheck(ctx context.Context, deps attachDoctorDeps) (service.DoctorCheck, bool) {
	if deps.glue == nil {
		return service.DoctorCheck{}, false
	}
	detail, fix, ok := glueDeclineFix(deps.glue(ctx))
	if !ok {
		return service.DoctorCheck{}, false
	}
	return service.DoctorCheck{ID: "glue.tracked-store", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: detail, Fix: fix}
	}}, true
}

// strandedQueueCheck registers glue.stranded-queue when this directory's board
// is not listed on this host but its store holds attach rows queued from a
// sandbox. The glue never lists a board by itself (repository content must not
// activate a host), so the rows wait for listing from a normal shell.
func strandedQueueCheck(ctx context.Context, deps attachDoctorDeps) (service.DoctorCheck, bool) {
	if deps.glue == nil || deps.pending == nil {
		return service.DoctorCheck{}, false
	}
	decision := deps.glue(ctx)
	if decision.Reason != attach.ReasonUnlisted {
		return service.DoctorCheck{}, false
	}
	rows, err := deps.pending(ctx, decision)
	if err != nil || len(rows) == 0 {
		return service.DoctorCheck{}, false
	}
	detail := fmt.Sprintf("%d attach queue row(s) from a sandboxed registration wait because this board is not listed on this host; harness glue does not list a board by itself", len(rows))
	return service.DoctorCheck{ID: "glue.stranded-queue", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: detail, Fix: strandedRegisterFix(rows)}
	}}, true
}

// productionPendingRows reads the attach queue of the store the predicate
// resolved, and only when it is the board doctor itself opened: doctor never
// opens a second store to look for rows.
func productionPendingRows(ctx context.Context, d attach.GlueDecision) ([]sqlite.AttachPending, error) {
	if app.store == nil || app.mtixDir == "" || d.StoreRoot == "" {
		return nil, nil
	}
	opened, err := filepath.EvalSymlinks(filepath.Dir(app.mtixDir))
	if err != nil || opened != d.StoreRoot {
		return nil, nil //nolint:nilerr // a board doctor did not open has no rows to report
	}
	rows, err := app.store.ListAttachPending(ctx)
	if err != nil {
		return nil, fmt.Errorf("list attach queue: %w", err)
	}
	return rows, nil
}
