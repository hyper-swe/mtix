// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestListenHoldArmed_Table(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	base := ContactInputs{
		Mode:         HubContactMode{Kind: HubContactActivity},
		Listen:       ListenSettings{Hold: true, Interval: 10 * time.Second, MaxIdle: 8 * time.Hour},
		LastActivity: t0, LiveLease: true,
	}
	tests := []struct {
		name   string
		mutate func(*ContactInputs)
		at     time.Time
		want   bool
	}{
		{"armed inside the clamp", func(*ContactInputs) {}, t0.Add(time.Hour), true},
		{"clamp passed", func(*ContactInputs) {}, t0.Add(8 * time.Hour), false},
		{"manual never holds", func(in *ContactInputs) { in.Mode = HubContactMode{Kind: HubContactManual} }, t0, false},
		{"listen off", func(in *ContactInputs) { in.Listen.Hold = false }, t0, false},
		{"no lease", func(in *ContactInputs) { in.LiveLease = false }, t0, false},
		{"no activity", func(in *ContactInputs) { in.LastActivity = time.Time{} }, t0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			tt.mutate(&in)
			got, deadline := ListenHoldArmed(in, tt.at)
			require.Equal(t, tt.want, got)
			if tt.want {
				require.True(t, deadline.Equal(t0.Add(8*time.Hour)))
			}
		})
	}
}
