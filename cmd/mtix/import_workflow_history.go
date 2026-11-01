// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// writeImportWorkflowReport preserves the human refusal on stderr and exposes
// its per-side history to --json callers even though no import result exists.
func writeImportWorkflowReport(report *sqlite.ImportReconcileReport) error {
	_, err := fmt.Fprint(os.Stderr, report.String())
	if err == nil && app.jsonOutput && !report.Applied && len(report.WorkflowConflicts) > 0 {
		err = json.NewEncoder(os.Stdout).Encode(struct {
			Applied   bool                      `json:"applied"`
			Conflicts []sqlite.WorkflowConflict `json:"workflow_conflicts"`
		}{Applied: false, Conflicts: report.WorkflowConflicts})
	}
	if err != nil {
		err = fmt.Errorf("write import report: %w", err)
		if !report.Applied {
			return nothingWritten(err)
		}
		return err
	}
	return nil
}

// writeImportReport publishes the report and remap while preserving whether
// the import already applied when an output error is returned.
func writeImportReport(report *sqlite.ImportReconcileReport, remapFile string) error {
	if report == nil {
		return nil
	}
	if err := writeImportWorkflowReport(report); err != nil {
		return err
	}
	if err := writeRemapFile(remapFile, report); err != nil {
		if !report.Applied {
			return nothingWritten(err)
		}
		return err
	}
	return nil
}
