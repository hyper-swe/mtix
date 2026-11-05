// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/service"
)

// trackedStoreFix is the exact remedy for a store the git index tracks.
const trackedStoreFix = "git rm -r --cached --ignore-unmatch .mtix/data && echo .mtix/data/ >> .gitignore"

// unreadableIndexFix is the remedy for an index layout the glue reader refuses.
const unreadableIndexFix = "git update-index --no-split-index && git sparse-checkout reapply --no-sparse-index"

// trackedStoreCheck registers the glue.tracked-store row for the directory
// doctor runs in, only when the glue activation predicate declined because the
// store is committed or the git index could not be read (MTIX-112.19.8, ADR-007
// A5). The glue stays off there (fail closed) until the row is green.
func trackedStoreCheck(ctx context.Context, deps attachDoctorDeps) (service.DoctorCheck, bool) {
	if deps.glue == nil {
		return service.DoctorCheck{}, false
	}
	decision := deps.glue(ctx)
	var detail, fix string
	switch decision.Reason {
	case attach.ReasonTrackedStore:
		detail = "the board store is tracked by git, so harness glue does nothing in this checkout"
		fix = trackedStoreFix
	case attach.ReasonIndexUnsupported:
		detail = "the git index uses a layout or size the glue reader refuses (unknown version or extension, sparse directory covering .mtix/, or oversize), so harness glue does nothing in this checkout"
		fix = unreadableIndexFix
	default:
		return service.DoctorCheck{}, false
	}
	return service.DoctorCheck{ID: "glue.tracked-store", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: detail, Fix: fix}
	}}, true
}
