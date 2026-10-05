// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Deterministic checks for the relay latency gate's sampling and failures.
package benchmarks

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRelayPublishLatency_OutliersAndRegressions(t *testing.T) {
	fast, slow := 500*time.Microsecond, 2*time.Millisecond
	cases := []struct {
		name    string
		samples []time.Duration
		want    time.Duration
		wantErr bool
	}{
		{"isolated first outlier", []time.Duration{slow, fast, fast, fast, fast, fast, fast}, fast, false},
		{"three outliers", []time.Duration{slow, fast, slow, fast, slow, fast, fast}, fast, false},
		{"majority slow", []time.Duration{fast, slow, fast, slow, fast, slow, slow}, slow, true},
		{"all slow", []time.Duration{slow, slow, slow, slow, slow, slow, slow}, slow, true},
		{"at budget", []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}, time.Millisecond, false},
		{"above budget", []time.Duration{time.Millisecond + time.Nanosecond, time.Millisecond + time.Nanosecond, time.Millisecond + time.Nanosecond, time.Millisecond + time.Nanosecond, time.Millisecond + time.Nanosecond, time.Millisecond + time.Nanosecond, time.Millisecond + time.Nanosecond}, time.Millisecond + time.Nanosecond, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, err := checkRelayPublishLatency(func() (int, time.Duration, error) {
				require.Less(t, calls, len(tc.samples))
				elapsed := tc.samples[calls] * 200
				calls++
				return 200, elapsed, nil
			})
			require.Equal(t, tc.wantErr, err != nil)
			require.Equal(t, tc.want, got)
			require.Equal(t, 7, calls, "must measure seven actual batches")
		})
	}
}

func TestRelayPublishLatency_InvalidBatchFails(t *testing.T) {
	publishErr := errors.New("publish failed")
	cases := []struct {
		name string
		n    int
		err  error
	}{
		{"empty", 0, nil},
		{"partial", 199, nil},
		{"extra", 201, nil},
		{"error", 200, publishErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			_, err := checkRelayPublishLatency(func() (int, time.Duration, error) {
				calls++
				if calls == 4 {
					return tc.n, time.Millisecond, tc.err
				}
				return 200, time.Millisecond, nil
			})
			require.Error(t, err)
			if tc.err != nil {
				require.ErrorIs(t, err, publishErr)
			}
			require.Equal(t, 4, calls, "invalid later batches cannot hide behind a fast first batch")
		})
	}
}
