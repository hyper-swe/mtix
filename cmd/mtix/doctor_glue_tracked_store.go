// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/service"
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
// activate a host), so the rows wait for listing from a normal shell. Rows are
// untrusted: only rows this store owns reach a printed command, and the rest
// are reported by count.
func strandedQueueCheck(ctx context.Context, deps attachDoctorDeps) (service.DoctorCheck, bool) {
	if deps.glue == nil || deps.pending == nil {
		return service.DoctorCheck{}, false
	}
	decision := deps.glue(ctx)
	if decision.Reason != attach.ReasonUnlisted {
		return service.DoctorCheck{}, false
	}
	rows, err := deps.pending(ctx, decision)
	if err != nil || len(rows.Valid)+rows.Rejected == 0 {
		return service.DoctorCheck{}, false
	}
	detail := fmt.Sprintf("%d attach queue row(s) from a sandboxed registration wait because this board is not listed on this host; harness glue does not list a board by itself", len(rows.Valid))
	if rows.Rejected > 0 {
		detail += fmt.Sprintf("; %d queued row(s) rejected because their board or lane does not belong to this store, and no command is built from them", rows.Rejected)
	}
	return service.DoctorCheck{ID: "glue.stranded-queue", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: detail, Fix: strandedRegisterFix(rows.Valid)}
	}}, true
}

// storeIdentityCheck registers glue.store-identity when the directory's board
// is listed but its store has a different identity than the listing records
// (the store was recreated or replaced). Glue stays off until the listing and
// the store agree again (MTIX-112.19.8).
func storeIdentityCheck(ctx context.Context, deps attachDoctorDeps) (service.DoctorCheck, bool) {
	if deps.glue == nil {
		return service.DoctorCheck{}, false
	}
	decision := deps.glue(ctx)
	if decision.Reason != attach.ReasonIdentityMismatch {
		return service.DoctorCheck{}, false
	}
	const detail = "this board is listed on this host, but its store has a different store identity than the listing records (the store was recreated or replaced), so harness glue does nothing in this checkout"
	// No mtix command re-lists a board under a new store identity: a listing
	// keeps the identity it was made with.
	const fix = "restore the original store file (.mtix/data/mtix.db) from a backup or from the previous copy of this checkout; mtix has no command that re-lists a recreated store, so glue stays off here until the original store is back"
	return service.DoctorCheck{ID: "glue.store-identity", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: detail, Fix: fix}
	}}, true
}
