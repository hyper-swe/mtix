// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// writeImportWorkflowReport preserves the human refusal on stderr and exposes
// its per-side history to --json callers even though no import result exists.
// Both are streamed from the report, one entry at a time (MTIX-2.3.2.2).
func writeImportWorkflowReport(report sqlite.ReportSource) error {
	err := report.WriteReport(os.Stderr)
	if err == nil && app.jsonOutput {
		err = report.WriteWorkflowJSON(os.Stdout)
	}
	if err != nil {
		err = fmt.Errorf("write import report: %w", err)
		if !report.IsApplied() {
			return nothingWritten(err)
		}
		return err
	}
	return nil
}

// writeImportReport publishes the report and remap while preserving whether
// the import already applied when an output error is returned. The report
// may be the in-memory one or a plan read from the staged export: neither is
// built as a whole here.
func writeImportReport(report sqlite.ReportSource, remapFile string) error {
	if report == nil {
		return nil
	}
	if err := writeImportWorkflowReport(report); err != nil {
		return err
	}
	if err := writeRemapFile(remapFile, report); err != nil {
		if !report.IsApplied() {
			return nothingWritten(err)
		}
		return err
	}
	return nil
}
