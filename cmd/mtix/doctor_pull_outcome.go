// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

func attachHubContactOutcome(row *DoctorCheck, src *hubContactSource) {
	c, present, err := src.state.ReadHubContact(src.key())
	if err != nil {
		row.Pass = false
		addRowFix(row, fixOperatorHome)
		return
	}
	if !present {
		return
	}
	row.LastContact = &c
	if c.OK {
		row.Detail += "; last pull succeeded"
		return
	}
	row.Pass = false
	row.Detail += "; last pull failed (" + c.ErrorClass + "); the next successful daemon pull clears this outcome"
	addRowFix(row, daemonPullFix(c.ErrorClass))
}

func daemonPullFix(class string) string {
	switch class {
	case "auth":
		return "repair the hub credential in MTIX_SYNC_DSN or .mtix/secrets, then run: mtix sync pull"
	case "refused":
		return "ask the hub operator to restore pull permissions and verified TLS configuration, then run: mtix sync pull"
	case "unreachable":
		return "restore network reachability to the configured hub, then run: mtix sync pull"
	case "timeout":
		return "check hub availability and query load, then run: mtix sync pull"
	default:
		return "run: mtix sync pull; then run: mtix sync doctor"
	}
}
