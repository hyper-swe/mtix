// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import "github.com/hyper-swe/mtix/internal/oplocal"

func appendOperatorPlacementCheck(report DoctorReport) DoctorReport {
	return appendDoctorCheck(report, DoctorCheck{Name: "operator state placement", Pass: true, Warn: true, Detail: oplocal.PlacementLimit, Fix: "Keep the operator configuration directory outside every sandbox-writable root. Unix refuses group-writable homes and network or directory-service-owned parents that do not meet its ownership rules. Set XDG_CONFIG_HOME to an operator-owned directory with root- or operator-owned ancestors and no group or other write access; on Windows choose APPDATA with supported ancestor access. Run mtix hooks status --json for the cause and to validate the new placement."})
}
