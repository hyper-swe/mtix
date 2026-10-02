// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// checkWorkflowFlags rejects --prefer, --theirs and --ours on a replace
// import, which takes the file whole and has no conflicts to settle, and an
// unknown --prefer value (MTIX-95.31.13).
func checkWorkflowFlags(f importFlags) error {
	if f.prefer != "" && f.prefer != string(sqlite.WorkflowTheirs) && f.prefer != string(sqlite.WorkflowOurs) {
		return fmt.Errorf("--prefer must be theirs or ours, not %q: %w", f.prefer, model.ErrInvalidInput)
	}
	if f.mode == "replace" && (f.prefer != "" || len(f.theirs) > 0 || len(f.ours) > 0) {
		return fmt.Errorf("--prefer, --theirs and --ours apply to --mode merge only; "+
			"a replace takes every value of the file: %w", model.ErrInvalidInput)
	}
	return nil
}

// trimIDs returns ids without surrounding spaces or empty entries.
func trimIDs(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// importModeFor validates the workflow flags (nothing is written when they
// are invalid) and returns the import mode; a replace passes the MTIX-90
// gate first (MTIX-95.31.13).
func importModeFor(ctx context.Context, exportData *sqlite.ExportData, f importFlags) (sqlite.ImportMode, error) {
	if flagErr := checkWorkflowFlags(f); flagErr != nil {
		return "", nothingWritten(flagErr)
	}
	if f.mode != "replace" {
		return sqlite.ImportModeMerge, nil
	}
	if guardErr := guardImportReplace(ctx, exportData); guardErr != nil {
		return "", guardErr
	}
	return sqlite.ImportModeReplace, nil
}

// workflowResolution returns the caller's answer to workflow conflicts from
// the --prefer, --theirs and --ours flags (MTIX-95.31.13).
func workflowResolution(f importFlags) sqlite.WorkflowResolution {
	return sqlite.WorkflowResolution{
		Prefer: sqlite.WorkflowChoice(f.prefer), Theirs: trimIDs(f.theirs), Ours: trimIDs(f.ours),
	}
}
