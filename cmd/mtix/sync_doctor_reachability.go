// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// doctorHubUnreachable gates dependent diagnostics only on a failed reachability
// check from this run (FR-18, MTIX-217). No PG row means no reachability claim:
// relay-only diagnosis and callers collecting local checks keep their behavior.
func doctorHubUnreachable(report DoctorReport) bool {
	for _, check := range report.Checks {
		if check.Name == "PG reachable" && !check.Pass && !check.Skipped {
			return true
		}
	}
	return false
}

func skippedHubCheck(name string) DoctorCheck {
	return DoctorCheck{Name: name, Skipped: true, Detail: "skipped (hub unreachable); fix PG reachable, then run mtix sync doctor again"}
}
