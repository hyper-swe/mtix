// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

// safeHubContact limits persisted and displayed data to known labels. Old
// success records remain success; an unclassified failure gets a safe fallback (MTIX-112.20.7).
func safeHubContact(c HubContact) HubContact {
	if c.Route != "poll" && c.Route != "pull" {
		c.Route = "unknown"
	}
	if c.OK {
		c.ErrorClass = ""
		return c
	}
	switch c.ErrorClass {
	case "auth", "refused", "tls", "unreachable", "timeout", "pull_failed":
	default:
		c.ErrorClass = "pull_failed"
	}
	return c
}
