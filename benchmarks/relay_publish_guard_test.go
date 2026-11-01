// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Synthetic durations test arithmetic boundaries; they are not calibration data.
package benchmarks

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func uniformRelayCosts(reference, candidate time.Duration) [7][2]time.Duration {
	var result [7][2]time.Duration
	for i := range result {
		result[i] = [2]time.Duration{reference, candidate}
	}
	return result
}

func relayCostsWithOutliers(indices ...int) [7][2]time.Duration {
	result := uniformRelayCosts(20*time.Millisecond, 20*time.Millisecond)
	for _, i := range indices {
		result[i][1] *= 2
	}
	return result
}

func TestEvaluateRelayPublishCosts_RelativeAndReferencePolicies(t *testing.T) {
	cases := []struct {
		name      string
		costs     [7][2]time.Duration
		quiet     bool
		wantRatio float64
		wantError string
	}{
		{"equal fast", uniformRelayCosts(20*time.Millisecond, 20*time.Millisecond), false, 1, ""},
		{"synthetic doubled ratio below ceiling", uniformRelayCosts(20*time.Millisecond, 40*time.Millisecond), false, 2, "relative"},
		{"three candidate outliers", relayCostsWithOutliers(0, 2, 4), false, 1, ""},
		{"majority candidate slowdown", relayCostsWithOutliers(0, 2, 4, 6), false, 2, "relative"},
		{"at ratio boundary", uniformRelayCosts(20*time.Millisecond, 30*time.Millisecond), false, 1.5, ""},
		{"above ratio by one ns", uniformRelayCosts(20*time.Millisecond, 30*time.Millisecond+time.Nanosecond), false, 1.5, "relative"},
		{"common load above ceiling", uniformRelayCosts(400*time.Millisecond, 400*time.Millisecond), false, 1, ""},
		{"quiet common slowdown", uniformRelayCosts(400*time.Millisecond, 400*time.Millisecond), true, 1, "reference"},
		{"quiet exact reference ceiling", uniformRelayCosts(200*time.Millisecond, 200*time.Millisecond), true, 1, ""},
		{"quiet fractional excess", uniformRelayCosts(200*time.Millisecond+time.Nanosecond, 200*time.Millisecond+time.Nanosecond), true, 1, "reference"},
		{"reference ceiling belongs only to reference", uniformRelayCosts(200*time.Millisecond, 300*time.Millisecond), true, 1.5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := evaluateRelayPublishCosts(tc.costs, 1.5, tc.quiet)
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
			require.InDelta(t, tc.wantRatio, got.MedianRatio, 0.000001)
		})
	}
}

func TestEvaluateRelayPublishCosts_UsesPairedRatios(t *testing.T) {
	// Both unpaired medians equal100ms, but six of seven actual pairs doubled.
	// Dividing the two medians would normalize away the relative regression.
	costs := [7][2]time.Duration{
		{time.Millisecond, 2 * time.Millisecond},
		{time.Millisecond, 2 * time.Millisecond},
		{time.Millisecond, 2 * time.Millisecond},
		{100 * time.Millisecond, 100 * time.Millisecond},
		{100 * time.Millisecond, 200 * time.Millisecond},
		{100 * time.Millisecond, 200 * time.Millisecond},
		{100 * time.Millisecond, 200 * time.Millisecond},
	}
	got, err := evaluateRelayPublishCosts(costs, 1.5, false)
	require.ErrorContains(t, err, "relative")
	require.Equal(t, float64(2), got.MedianRatio)
	require.Equal(t, 100*time.Millisecond, got.ReferenceBatch)
	require.Equal(t, 100*time.Millisecond, got.CandidateBatch)
}

func TestEvaluateRelayPublishCosts_RejectsInvalidDurations(t *testing.T) {
	cases := []struct {
		name string
		row  int
		side int
		bad  time.Duration
	}{
		{"first reference zero", 0, 0, 0},
		{"last reference negative", 6, 0, -time.Nanosecond},
		{"first candidate negative", 0, 1, -time.Nanosecond},
		{"last candidate zero", 6, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			costs := uniformRelayCosts(time.Millisecond, time.Millisecond)
			costs[tc.row][tc.side] = tc.bad
			_, err := evaluateRelayPublishCosts(costs, 1.5, false)
			require.ErrorContains(t, err, "positive")
		})
	}
}

func TestEvaluateRelayPublishCosts_RejectsInvalidLimit(t *testing.T) {
	cases := []struct {
		name  string
		limit float64
	}{
		{"zero", 0}, {"negative", -1}, {"nan", math.NaN()},
		{"positive infinity", math.Inf(1)}, {"negative infinity", math.Inf(-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := evaluateRelayPublishCosts(uniformRelayCosts(time.Millisecond, time.Millisecond), tc.limit, false)
			require.ErrorContains(t, err, "ratio limit")
		})
	}
}

func TestValidateRelayPublishMeasurement_RejectsInvalidInput(t *testing.T) {
	publicationErr := errors.New("publication failed")
	cases := []struct {
		name    string
		count   int
		elapsed time.Duration
		err     error
		wantErr bool
	}{
		{"complete positive", 200, time.Nanosecond, nil, false},
		{"empty", 0, time.Millisecond, nil, true},
		{"negative count", -1, time.Millisecond, nil, true},
		{"partial", 199, time.Millisecond, nil, true},
		{"extra", 201, time.Millisecond, nil, true},
		{"zero timing", 200, 0, nil, true},
		{"negative timing", 200, -time.Nanosecond, nil, true},
		{"error", 200, time.Millisecond, publicationErr, true},
	}
	for _, side := range []string{"reference", "candidate"} {
		for _, tc := range cases {
			t.Run(side+"/"+tc.name, func(t *testing.T) {
				err := validateRelayPublishMeasurement(side, tc.count, tc.elapsed, tc.err)
				if tc.wantErr {
					require.ErrorContains(t, err, side)
				} else {
					require.NoError(t, err)
				}
				if tc.err != nil {
					require.ErrorIs(t, err, publicationErr)
				}
			})
		}
	}
}

func TestRelayQuietModeError_OnlyExplicitQuietModeRejectsRaceOrShort(t *testing.T) {
	cases := []struct {
		quiet, race, short, wantErr bool
	}{
		{false, false, false, false},
		{false, false, true, false},
		{false, true, false, false},
		{false, true, true, false},
		{true, false, false, false},
		{true, false, true, true},
		{true, true, false, true},
		{true, true, true, true},
	}
	for _, tc := range cases {
		err := relayQuietModeError(tc.quiet, tc.race, tc.short)
		if tc.wantErr {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
}

func TestCheckRelayPublishLatency_CollectsSevenAlternatingPairs(t *testing.T) {
	calls := 0
	got, err := checkRelayPublishLatency(func(referenceFirst bool) relayPublishPair {
		require.Equal(t, calls%2 == 0, referenceFirst)
		calls++
		measurement := relayPublishMeasurement{Count: 200, Elapsed: time.Millisecond}
		return relayPublishPair{Reference: measurement, Candidate: measurement, ReferenceFirst: referenceFirst}
	}, false)
	require.NoError(t, err)
	require.Equal(t, 7, calls)
	require.Equal(t, float64(1), got.MedianRatio)
}

func TestCheckRelayPublishLatency_RejectsInvalidLaterPairs(t *testing.T) {
	publicationErr := errors.New("failed actual operation")
	for _, side := range []string{"reference", "candidate"} {
		for _, bad := range []relayPublishMeasurement{
			{Count: 199, Elapsed: time.Millisecond},
			{Count: 200},
			{Count: 200, Elapsed: time.Millisecond, Err: publicationErr},
		} {
			t.Run(fmt.Sprintf("%s/count%d/time%d/error%v", side, bad.Count, bad.Elapsed, bad.Err), func(t *testing.T) {
				calls := 0
				_, err := checkRelayPublishLatency(func(referenceFirst bool) relayPublishPair {
					calls++
					good := relayPublishMeasurement{Count: 200, Elapsed: time.Millisecond}
					pair := relayPublishPair{Reference: good, Candidate: good, ReferenceFirst: referenceFirst}
					if calls == 4 {
						if side == "reference" {
							pair.Reference = bad
						} else {
							pair.Candidate = bad
						}
					}
					return pair
				}, false)
				require.ErrorContains(t, err, side)
				if bad.Err != nil {
					require.ErrorIs(t, err, publicationErr)
				}
				require.Equal(t, 4, calls)
			})
		}
	}
}

func TestCheckRelayPublishLatency_RejectsIncorrectOrder(t *testing.T) {
	calls := 0
	_, err := checkRelayPublishLatency(func(referenceFirst bool) relayPublishPair {
		calls++
		return relayPublishPair{ReferenceFirst: !referenceFirst}
	}, false)
	require.ErrorContains(t, err, "timing order")
	require.Equal(t, 1, calls)
}
