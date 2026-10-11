// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Receipt diagnostics load local provenance without deciding lineage.
package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func appendExchangeRecordCheck(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	check := DoctorCheck{Name: "sync.exchange-record", Pass: true}
	if st == nil {
		check.Warn = true
		check.Detail = "local store not initialized"
		return appendDoctorCheck(r, check)
	}
	status, err := exchangeService(st).ExchangeRecordStatus(ctx, app.mtixDir)
	if err != nil {
		check.Pass = false
		check.Detail = "receipt diagnostics unavailable"
		return appendDoctorCheck(r, check)
	}
	if status.Present {
		check.Detail = fmt.Sprintf("%s %s %s", status.WrittenAt, status.Source, status.BoardForm)
	} else {
		check.Detail = "no received record"
		check.Warn = true
	}
	if status.Reason != "" {
		check.Warn = true
		if status.Reason == "outbound-boundary-unknown" {
			check.Detail += "; inferred legacy receipt has unknown outbound state: " + status.Reason
		} else {
			check.Detail += "; recorded omission (may be stale): " + status.Reason
		}
		if status.Reason == "pending-outbound" || status.Reason == "outbound-boundary-unknown" {
			check.Fix = "Run mtix sync push, then a new, complete mtix sync pull to record a boundary with no pending outbound events."
		}
	}
	return appendDoctorCheck(r, check)
}
