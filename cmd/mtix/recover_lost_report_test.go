// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestPrintRecoverReport_LostIDsListedOnce_CountMatchesTheList pins the LOST
// line of the salvage report (MTIX-107.80): the count printed is the number of
// ids in the list, so a lost id listed once reads 1, never 2.
func TestPrintRecoverReport_LostIDsListedOnce_CountMatchesTheList(t *testing.T) {
	res := &sqlite.RecoverResult{
		Export:  &sqlite.ExportData{NodeCount: 3},
		LostIDs: []string{"RC-2"},
	}
	out := captureStdout(t, func() { printRecoverReport(res, "recovered.json") })
	assert.Contains(t, out, "LOST          : 1 — [RC-2]")
	assert.NotContains(t, out, "[RC-2 RC-2]")

	res.LostIDs = []string{"RC-2", "RC-5"}
	out = captureStdout(t, func() { printRecoverReport(res, "recovered.json") })
	assert.Contains(t, out, "LOST          : 2 — [RC-2 RC-5]")
}
