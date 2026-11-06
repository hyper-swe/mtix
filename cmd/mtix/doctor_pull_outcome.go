// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// attachHubContactOutcome keeps disabled policies truthful without a permanent failure (MTIX-112.20.7).
func attachHubContactOutcome(row *DoctorCheck, src *hubContactSource, hub, manual bool) {
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
	row.Detail += "; last pull failed (" + c.ErrorClass + ")"
	if !hub {
		row.Detail += "; historical outcome"
		return
	}
	if manual {
		row.Warn = true
		row.Detail += "; manual mode: automatic retry is disabled"
	} else {
		row.Pass = false
	}
	addRowFix(row, daemonPullFix(c.ErrorClass))
}

// daemonPullFix names a recovery matching the recorded safe class (MTIX-112.20.7).
func daemonPullFix(class string) string {
	switch class {
	case "auth":
		return "repair the hub credential in MTIX_SYNC_DSN, MTIX_SYNC_PASSWORD or .mtix/secrets, then run: mtix sync pull"
	case "refused":
		return "ask the hub operator to restore pull permissions, then run: mtix sync pull"
	case "tls":
		return "set sslmode=verify-full in the configured DSN and supply sslrootcert for its trusted CA, then run: mtix sync pull"
	case "unreachable":
		return "restore network reachability to the configured hub, then run: mtix sync pull"
	case "timeout":
		return "check hub availability and query load, then run: mtix sync pull"
	default:
		return "run: mtix sync pull; then run: mtix sync doctor"
	}
}
