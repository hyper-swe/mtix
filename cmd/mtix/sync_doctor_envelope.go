// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

func appendEnvelopeCapsCheck(report DoctorReport) DoctorReport {
	check := DoctorCheck{Name: "inbox envelope caps", Pass: true, Detail: "max_events=0 max_body_bytes=0 max_total_bytes=0 (unlimited)"}
	env, err := oplocal.FromProcess()
	if err != nil {
		check.Pass = false
		check.Detail = err.Error()
		check.Fix = "Restore the operator configuration directory and run mtix sync doctor --json."
		return appendDoctorCheck(report, check)
	}
	state := oplocal.New(env)
	caps, reports, err := state.EnvelopeCaps()
	if err != nil {
		check.Pass = false
		check.Detail = err.Error()
		check.Fix = state.EnvelopeCapsRemedy(reports...)
	} else {
		check.Detail = fmt.Sprintf("max_events=%d max_body_bytes=%d max_total_bytes=%d (0 means unlimited)", caps.MaxEvents, caps.MaxBodyBytes, caps.MaxTotalBytes)
	}
	return appendDoctorCheck(report, check)
}
