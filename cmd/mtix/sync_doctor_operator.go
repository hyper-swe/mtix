// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import "github.com/hyper-swe/mtix/internal/oplocal"

func appendOperatorPlacementCheck(report DoctorReport) DoctorReport {
	return appendDoctorCheck(report, DoctorCheck{Name: "operator state placement", Pass: true, Warn: true, Detail: oplocal.PlacementLimit, Fix: "Keep the operator configuration directory outside every sandbox-writable root; run mtix hooks status --json to validate its path and access rules."})
}
