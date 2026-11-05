// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Remedies, one per decline cause (MTIX-112.19.8). The git commands were run
// against real git: the tracked-store one also drops a split index, because a
// split index's shared base keeps listing the store after git rm --cached.
const (
	trackedStoreFix    = "git rm -r --cached --ignore-unmatch .mtix/data && git update-index --no-split-index && echo .mtix/data/ >> .gitignore"
	trackedAncestorFix = "git update-index --force-remove -- .mtix .mtix/data && git update-index --no-split-index"
	sparseIndexFix     = "git sparse-checkout reapply --no-sparse-index"
	splitBaseFix       = "git read-tree HEAD   # rebuilds the index from HEAD; commit or stash staged changes first"
	unsupportedFix     = "upgrade mtix (mtix version shows this build): it cannot read this git index layout, and glue stays off in this checkout until it can"
)

// glueDeclineFix maps a decline (reason and cause) to its detail and remedy; ok
// is false for reasons that are not a problem to report.
func glueDeclineFix(d attach.GlueDecision) (detail, fix string, ok bool) {
	switch d.Reason {
	case attach.ReasonTrackedStore:
		if d.Cause == attach.TrackedAncestor {
			return "git tracks a symlink or submodule entry at .mtix or .mtix/data, so harness glue does nothing in this checkout", trackedAncestorFix, true
		}
		return "the board store is tracked by git, so harness glue does nothing in this checkout", trackedStoreFix, true
	case attach.ReasonIndexUnsupported:
		return indexCauseFix(d.Cause)
	}
	return "", "", false
}

func indexCauseFix(cause string) (detail, fix string, ok bool) {
	switch cause {
	case attach.CauseSparse:
		return "the git index is a sparse index with a directory entry covering .mtix, which mtix refuses to guess about, so harness glue does nothing in this checkout", sparseIndexFix, true
	case attach.CauseSplitBase:
		return "the git index is a split index whose shared base file is missing, so harness glue does nothing in this checkout", splitBaseFix, true
	case attach.CauseOversize:
		return "the git index is larger than the size mtix reads, so harness glue does nothing in this checkout", unsupportedFix, true
	case attach.CauseExtension, attach.CauseVersion:
		return "the git index uses a version or extension this mtix does not know, so harness glue does nothing in this checkout", unsupportedFix, true
	}
	return "the git index could not be parsed (for example a SHA-256 repository), so harness glue does nothing in this checkout", unsupportedFix, true
}

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
	if deps.glue == nil || deps.pending == nil || deps.glue(ctx).Reason != attach.ReasonUnlisted {
		return service.DoctorCheck{}, false
	}
	rows, err := deps.pending(ctx)
	if err != nil || len(rows) == 0 {
		return service.DoctorCheck{}, false
	}
	detail := fmt.Sprintf("%d attach queue row(s) from a sandboxed registration wait because this board is not listed on this host; harness glue does not list a board by itself", len(rows))
	return service.DoctorCheck{ID: "glue.stranded-queue", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: detail,
			Fix: "mtix agent register <agent-id>   # run it for the agent that registered, from a normal shell in this checkout (outside any sandbox), so the host lists the board"}
	}}, true
}

// productionPendingRows reads the attach queue of the board doctor runs in.
func productionPendingRows(ctx context.Context) ([]sqlite.AttachPending, error) {
	if app.store == nil {
		return nil, nil
	}
	rows, err := app.store.ListAttachPending(ctx)
	if err != nil {
		return nil, fmt.Errorf("list attach queue: %w", err)
	}
	return rows, nil
}
