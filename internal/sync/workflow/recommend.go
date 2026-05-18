// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"fmt"
	"strings"
)

// Severity classifies a recommendation's urgency.
type Severity string

const (
	SeverityInfo   Severity = "info"
	SeverityWarn   Severity = "warn"
	SeverityRefuse Severity = "refuse"
)

// Recommendation is one structured suggestion in the rendered output.
// All fields are static text or come from the closed allowedDocLinks
// list — never agent-injected.
type Recommendation struct {
	Action    string   `json:"action"`
	Rationale string   `json:"rationale"`
	DocLink   string   `json:"doc_link,omitempty"`
	Severity  Severity `json:"severity"`
}

// MaxRenderBytes caps the rendered output per FR-18.17 / MTIX-15.8.
// The mtix_sync_workflow MCP tool must never overwhelm an agent
// context window.
const MaxRenderBytes = 4096

// TruncationMarker appears verbatim when Render had to drop entries.
// Tests assert presence; do not change without updating the renderer
// regression test.
const TruncationMarker = "... (recommendations truncated)"

// allowedDocLinks is the closed set of paths Recommendation.DocLink
// may reference. Tests assert no recommendation uses an off-list link.
// Adding a link requires a code change, which is reviewable.
var allowedDocLinks = []string{
	"docs/SYNC-DESIGN.md#solo-mode",
	"docs/SYNC-DESIGN.md#bootstrap",
	"docs/SYNC-DESIGN.md#operations",
	"docs/SYNC-DESIGN.md#conflicts",
	"docs/SYNC-DESIGN.md#troubleshooting",
}

// RecommendForState returns the rule-based recommendation set for a
// given Report. Pure function — same input always yields the same
// output. Recommendations are constructed from string constants only;
// no DSN value, hostname, or PG error message ever flows into Action
// or Rationale text.
func RecommendForState(r Report) []Recommendation {
	switch r.State {
	case StateSolo:
		return recommendSolo()
	case StateSyncConfiguredNoHub:
		return recommendSyncConfiguredNoHub(r)
	case StateSyncActive:
		return recommendSyncActive(r)
	case StateDivergentPending:
		return recommendDivergent()
	case StateHubUnreachable:
		return recommendHubUnreachable()
	}
	return nil
}

func recommendSolo() []Recommendation {
	return []Recommendation{
		{
			Action:    "Continue working in solo mode",
			Rationale: "No sync hub is configured; mtix is fully functional locally.",
			DocLink:   "docs/SYNC-DESIGN.md#solo-mode",
			Severity:  SeverityInfo,
		},
		{
			Action:    "Run 'mtix sync init' when adding a second developer",
			Rationale: "Bootstrap a Postgres hub when the team grows. Configure DSN via MTIX_SYNC_DSN env or .mtix/secrets (mode 0600); never commit DSN to tracked files.",
			DocLink:   "docs/SYNC-DESIGN.md#bootstrap",
			Severity:  SeverityInfo,
		},
	}
}

func recommendSyncConfiguredNoHub(r Report) []Recommendation {
	out := []Recommendation{
		{
			Action:    "Run 'mtix sync init' to bootstrap a fresh hub",
			Rationale: "Use this if you control the Postgres hub. The init command pushes local history with project-prefix collision checks.",
			DocLink:   "docs/SYNC-DESIGN.md#bootstrap",
			Severity:  SeverityInfo,
		},
		{
			Action:    "Run 'mtix sync clone' if joining an existing hub",
			Rationale: "Use this if a teammate has already initialized the hub. Clone idempotently rebuilds local state from the hub event log.",
			DocLink:   "docs/SYNC-DESIGN.md#bootstrap",
			Severity:  SeverityInfo,
		},
	}
	// MTIX-15.13.1 upgrader detection: nodes exist but no events were
	// ever emitted. This is the v0.1.x → v0.2.0-beta migration shape.
	// Recommend backfill so existing history flows to the hub.
	if r.LocalNodeCount > 0 && r.LocalEventCount == 0 {
		out = append(out, Recommendation{
			Action:    "Run 'mtix sync backfill' to synthesize sync_events from existing nodes",
			Rationale: "This project has nodes but no sync events — typical of an upgrade from v0.1.x to v0.2.0-beta. Backfill walks the canonical tables and emits create/update/transition events so the next push populates the hub with your full history.",
			DocLink:   "docs/SYNC-DESIGN.md#bootstrap",
			Severity:  SeverityWarn,
		})
	}
	return out
}

func recommendSyncActive(r Report) []Recommendation {
	out := []Recommendation{{
		Action:    "Continue normal mtix workflow",
		Rationale: fmt.Sprintf("Sync is healthy: %d local events, %d applied from hub.", r.LocalEventCount, r.AppliedEventCount),
		DocLink:   "docs/SYNC-DESIGN.md#operations",
		Severity:  SeverityInfo,
	}}
	// We don't track precise last-push timestamps locally yet; the warn
	// path here is informational and never crosses into refuse semantics.
	return out
}

func recommendDivergent() []Recommendation {
	return []Recommendation{
		{
			Action:    "Run 'mtix sync conflicts list' to inspect divergence",
			Rationale: "Unresolved conflicts are present. Inspect them before continuing local edits to avoid extending the divergent state.",
			DocLink:   "docs/SYNC-DESIGN.md#conflicts",
			Severity:  SeverityWarn,
		},
		{
			Action:    "Then 'mtix sync conflicts resolve' or 'mtix sync reconcile --dry-run'",
			Rationale: "Per-conflict resolve is preferred for small divergence; reconcile (with --dry-run first) is the escape hatch for whole-project rename or discard.",
			DocLink:   "docs/SYNC-DESIGN.md#conflicts",
			Severity:  SeverityWarn,
		},
	}
}

func recommendHubUnreachable() []Recommendation {
	return []Recommendation{
		{
			Action:    "Run 'mtix sync doctor' to diagnose connectivity",
			Rationale: "Three or more consecutive sync errors indicate the hub may be unreachable. Doctor runs PG ping, schema check, and TLS posture checks.",
			DocLink:   "docs/SYNC-DESIGN.md#troubleshooting",
			Severity:  SeverityWarn,
		},
		{
			Action:    "Verify TLS posture (sslmode=verify-full required for non-loopback hosts)",
			Rationale: "Check that MTIX_SYNC_SSLROOTCERT points at a valid CA bundle. Loosening sslmode is not recommended.",
			DocLink:   "docs/SYNC-DESIGN.md#troubleshooting",
			Severity:  SeverityWarn,
		},
	}
}

// Render produces the human-and-agent-readable output for the MCP
// tool. Output is bounded by MaxRenderBytes; if the natural output
// would exceed that, the tail is replaced with TruncationMarker so
// the truncation is observable rather than silent.
//
// The function never embeds DSN, hostnames, or PG error strings —
// it composes only the Report counts and the recommendation slice.
func Render(r Report, recs []Recommendation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "State: %s\n", r.StateName)
	fmt.Fprintf(&b, "  has_dsn: %t\n", r.HasDSN)
	fmt.Fprintf(&b, "  local_events: %d\n", r.LocalEventCount)
	fmt.Fprintf(&b, "  applied_events: %d\n", r.AppliedEventCount)
	fmt.Fprintf(&b, "  unresolved_conflicts: %t\n", r.HasUnresolvedConflicts)
	fmt.Fprintf(&b, "  consecutive_errors: %d\n", r.ConsecutiveErrors)
	b.WriteString("\nRecommendations:\n")

	header := b.String()
	// Reserve space for the truncation marker so we can append it
	// without exceeding the cap.
	budget := MaxRenderBytes - len(header) - len(TruncationMarker) - 1
	if budget <= 0 {
		// Header alone wouldn't fit; degrade gracefully.
		end := len(header)
		if end > MaxRenderBytes {
			end = MaxRenderBytes
		}
		return header[:end]
	}

	var body strings.Builder
	truncated := false
	for i, rec := range recs {
		entry := formatRecommendation(i+1, rec)
		if body.Len()+len(entry) > budget {
			truncated = true
			break
		}
		body.WriteString(entry)
	}

	out := header + body.String()
	if truncated {
		out += TruncationMarker
	}
	return out
}

func formatRecommendation(idx int, rec Recommendation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %d. [%s] %s\n", idx, rec.Severity, rec.Action)
	if rec.Rationale != "" {
		fmt.Fprintf(&b, "     %s\n", rec.Rationale)
	}
	if rec.DocLink != "" {
		fmt.Fprintf(&b, "     see: %s\n", rec.DocLink)
	}
	return b.String()
}

