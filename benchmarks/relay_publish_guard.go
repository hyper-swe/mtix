// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package benchmarks

import (
	"fmt"
	"math"
	"slices"
	"time"
)

const (
	// The unchanged FR-21 §9 ceiling applies to the quiet reference measurement.
	relayPublishBudget = time.Millisecond
	// Frozen after 10 ordinary and 10 race seven-pair runs on Apple M5,
	// Go 1.26.9: highest run median 1.135569; actual doubled-work mutant
	// median 2.348368. This is observed headroom, not a universal noise bound.
	relayPublishMaxRatio = 1.5
)

type relayPublishSummary struct {
	MedianRatio    float64
	ReferenceBatch time.Duration
	CandidateBatch time.Duration
}

func validateRelayPublishMeasurement(side string, count int, elapsed time.Duration, publicationErr error) error {
	if publicationErr != nil {
		return fmt.Errorf("%s publication: %w", side, publicationErr)
	}
	if count != 200 {
		return fmt.Errorf("%s: got %d events, want 200", side, count)
	}
	if elapsed <= 0 {
		return fmt.Errorf("%s: elapsed time must be positive, got %v", side, elapsed)
	}
	return nil
}

// evaluateRelayPublishCosts takes the median of paired ratios, never the ratio
// of two medians. Common storage/serialization costs can cancel; the independent
// quiet reference ceiling provides the separate absolute check.
func evaluateRelayPublishCosts(costs [7][2]time.Duration, limit float64, quiet bool) (relayPublishSummary, error) {
	var result relayPublishSummary
	if limit <= 0 || math.IsNaN(limit) || math.IsInf(limit, 0) {
		return result, fmt.Errorf("ratio limit must be finite and positive: %v", limit)
	}
	var ratios [7]float64
	var references, candidates [7]time.Duration
	for i, pair := range costs {
		if pair[0] <= 0 || pair[1] <= 0 {
			return result, fmt.Errorf("pair %d: both durations must be positive", i+1)
		}
		references[i], candidates[i] = pair[0], pair[1]
		ratios[i] = float64(pair[1]) / float64(pair[0])
	}
	slices.Sort(ratios[:])
	slices.Sort(references[:])
	slices.Sort(candidates[:])
	result = relayPublishSummary{ratios[3], references[3], candidates[3]}
	if result.MedianRatio > limit {
		return result, fmt.Errorf("relay relative median ratio %.6f exceeds %.6f", result.MedianRatio, limit)
	}
	// Compare complete nanoseconds before per-event division can round an excess.
	if quiet && result.ReferenceBatch > relayPublishBudget*200 {
		return result, fmt.Errorf("relay reference batch %v exceeds %v (%v/event)", result.ReferenceBatch, relayPublishBudget*200, relayPublishBudget)
	}
	return result, nil
}

func relayQuietModeError(quiet, race, short bool) error {
	if quiet && (race || short) {
		return fmt.Errorf("relay-quiet requires a non-race, non-short run")
	}
	return nil
}
