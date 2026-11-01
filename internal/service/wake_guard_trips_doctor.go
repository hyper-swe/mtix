// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

// guardTripLimit bounds how many recent trips the doctor row lists.
const guardTripLimit = 20

// WakeGuardTripsReader is the read-only alert capability the guard-trips check needs.
type WakeGuardTripsReader interface {
	Recent(ctx context.Context, limit int) ([]oplocal.Alert, error)
}

// WakeGuardTripsDoctorCheck lists recent loop-guard trips (hop depth and back-off),
// newest first, with agent, chain, time and the exact fix command (FR-20.3, P1.7).
// It only reads operator alerts and never changes state.
func WakeGuardTripsDoctorCheck(alerts WakeGuardTripsReader) DoctorCheck {
	return DoctorCheck{ID: "wake.guard-trips", Run: func(ctx context.Context) DoctorResult {
		if alerts == nil {
			return DoctorResult{Status: DoctorFail, Detail: "Operator alerts are unavailable.", Fix: "Restore the operator configuration path, then run mtix doctor."}
		}
		rows, err := alerts.Recent(ctx, 0)
		if err != nil {
			return DoctorResult{Status: DoctorWarn, Detail: fmt.Sprintf("Cannot read operator alerts: %v", err),
				Fix: "Restore access to the operator configuration path, then run mtix doctor."}
		}
		return summarizeGuardTrips(rows)
	}}
}

var guardTripChainPattern = regexp.MustCompile(`\[chain: ([^\]]+)\]`)

// guardTripChain extracts the chain identity (triggering event and hop or unacked count)
// that the trip alert recorded; it is empty only for alerts raised by an older build.
func guardTripChain(detail string) string {
	match := guardTripChainPattern.FindStringSubmatch(detail)
	if match == nil {
		return ""
	}
	return match[1]
}

func summarizeGuardTrips(rows []oplocal.Alert) DoctorResult {
	// Newest first by last occurrence, whatever order the reader returned.
	rows = append([]oplocal.Alert(nil), rows...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastAt.After(rows[j].LastAt) })
	trips := make([]any, 0, guardTripLimit)
	var lines []string
	fix := ""
	for _, row := range rows {
		if row.Class != oplocal.AlertClassWakeHopDepth && row.Class != oplocal.AlertClassWakeBackoff {
			continue
		}
		if len(trips) == guardTripLimit {
			break
		}
		trips = append(trips, map[string]any{"agent": row.Agent, "class": row.Class, "chain": guardTripChain(row.Detail), "fix": row.Fix,
			"first_at": row.FirstAt.UTC().Format(time.RFC3339), "last_at": row.LastAt.UTC().Format(time.RFC3339), "count": row.Count})
		lines = append(lines, fmt.Sprintf("%s %s at %s (x%d) chain %s: %s; fix: %s", row.Agent, row.Class, row.LastAt.UTC().Format(time.RFC3339), row.Count, guardTripChain(row.Detail), row.Detail, row.Fix))
		if fix == "" {
			fix = row.Fix
		}
	}
	if len(trips) == 0 {
		return DoctorResult{Status: DoctorInfo, Detail: "No loop-guard trips recorded. Guards stay on; no tuning is needed."}
	}
	return DoctorResult{Status: DoctorWarn, Detail: fmt.Sprintf("%d recent loop-guard trips (newest first): %s", len(trips), strings.Join(lines, "; ")),
		Fix: fix, Data: map[string]any{"trips": trips}}
}
