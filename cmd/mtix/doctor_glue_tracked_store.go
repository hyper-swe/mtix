// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"path/filepath"

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
		return service.DoctorResult{Status: service.DoctorWarn, Detail: detail, Fix: strandedRegisterFix(decision.StoreRoot, rows.Valid)}
	}}, true
}

// storeIdentityCheck registers glue.store-identity when the directory's board
// is listed but its store does not match the listing. The detail and the fix
// depend on why (see identityDetailFix): the glue stays off until the listing
// and the store agree again (MTIX-112.19.8).
func storeIdentityCheck(ctx context.Context, deps attachDoctorDeps) (service.DoctorCheck, bool) {
	if deps.glue == nil {
		return service.DoctorCheck{}, false
	}
	decision := deps.glue(ctx)
	if decision.Reason != attach.ReasonIdentityMismatch {
		return service.DoctorCheck{}, false
	}
	detail, fix := identityDetailFix(decision)
	return service.DoctorCheck{ID: "glue.store-identity", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: detail, Fix: fix}
	}}, true
}

// identityDetailFix explains an identity-mismatch decline and names its
// recovery. Only a store whose identity differs from the one listed needs the
// original store back; a listing with no identity is completed by registering
// from a normal shell, and a store that cannot be read as a plain store needs
// its directories fixed (or any normal mtix command, if a side file is mid-open).
func identityDetailFix(d attach.GlueDecision) (detail, fix string) {
	switch d.Cause {
	case attach.CauseListingNoID:
		return "this board is listed on this host, but the listing records no store identity for its store, so harness glue does nothing in this checkout",
			"mtix agent register <agent-id>   # run from a normal shell in this checkout (outside any sandbox); it records the store identity in the listing, after which glue acts"
	case attach.CauseStoreUnreadable:
		return "this board is listed on this host, but its store cannot be read as a plain store (.mtix or .mtix/data is a symlink or missing, or the store file could not be read), so harness glue does nothing in this checkout",
			"make .mtix and .mtix/data real directories that hold the store file (replace a symlink with the directory it points to); if the store was just in use, run any normal mtix command (for example mtix doctor) and check again"
	}
	// No mtix command re-lists a board under a new store identity: a listing
	// keeps the identity it was made with.
	return "this board is listed on this host, but its store has a different store identity than the listing records (the store was recreated or replaced), so harness glue does nothing in this checkout",
		"restore the original store file (.mtix/data/mtix.db) from a backup or from the previous copy of this checkout; mtix has no command that re-lists a recreated store, so glue stays off here until the original store is back"
}

// checkoutCheck registers glue.checkout when the directory's board is listed
// but its .git entry stopped being a valid checkout (a linked worktree whose
// admin directory was pruned, a main checkout that moved, a junk .git file).
// The glue stays off until git can resolve the checkout again (MTIX-112.19.8).
func checkoutCheck(ctx context.Context, deps attachDoctorDeps) (service.DoctorCheck, bool) {
	if deps.glue == nil {
		return service.DoctorCheck{}, false
	}
	decision := deps.glue(ctx)
	if decision.Reason != attach.ReasonNoCheckout || decision.Cause != attach.CauseListedCheckout {
		return service.DoctorCheck{}, false
	}
	detail := fmt.Sprintf("this board is listed on this host, but the .git entry at %s is not a valid checkout (a linked worktree whose admin directory was removed, a main checkout that moved, or a damaged .git file), so harness glue does nothing in this checkout", filepath.Join(decision.StoreRoot, ".git"))
	fix := "git worktree repair " + shellQuote(decision.StoreRoot) + "   # run from the main checkout; if its worktree record was pruned, re-create the worktree with git worktree add instead; then run mtix doctor again"
	return service.DoctorCheck{ID: "glue.checkout", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: detail, Fix: fix}
	}}, true
}
