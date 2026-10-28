// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func TestParseHubContact_Table(t *testing.T) {
	tests := []struct {
		in      string
		kind    service.HubContactKind
		every   time.Duration
		wantErr bool
	}{
		{"activity", service.HubContactActivity, 0, false},
		{"", service.HubContactActivity, 0, false},
		{"manual", service.HubContactManual, 0, false},
		{"interval:30s", service.HubContactInterval, 30 * time.Second, false},
		{"interval:5m", service.HubContactInterval, 5 * time.Minute, false},
		{"interval:500ms", 0, 0, true},
		{"interval:", 0, 0, true},
		{"interval:abc", 0, 0, true},
		{"interval:-5s", 0, 0, true},
		{"always", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := service.ParseHubContact(tt.in)
			if tt.wantErr {
				require.ErrorIs(t, err, service.ErrInvalidHubContact)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.kind, got.Kind)
			require.Equal(t, tt.every, got.Interval)
		})
	}
}

func TestHubContactInterval_PrintsCostWarningOnce(t *testing.T) {
	m, err := service.ParseHubContact("interval:5s")
	require.NoError(t, err)
	w := service.NewCostWarner()
	first := w.Warn(m)
	require.Contains(t, first, "720")
	require.Contains(t, first, "scale-to-zero")
	require.Empty(t, w.Warn(m), "the warning prints once")

	act, _ := service.ParseHubContact("activity")
	require.Empty(t, service.NewCostWarner().Warn(act))
}

func TestParseListen_Table(t *testing.T) {
	for in, want := range map[string]bool{"live": true, "off": false} {
		got, err := service.ParseListen(in)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	_, err := service.ParseListen("on")
	require.ErrorIs(t, err, service.ErrInvalidHubContact)
}

func TestParseListenDurations(t *testing.T) {
	d, err := service.ParseListenInterval("10s")
	require.NoError(t, err)
	require.Equal(t, 10*time.Second, d)
	for _, bad := range []string{"0s", "500ms", "x", "-1s"} {
		_, err = service.ParseListenInterval(bad)
		require.ErrorIs(t, err, service.ErrInvalidHubContact, bad)
	}
	m, err := service.ParseListenMaxIdle("8h")
	require.NoError(t, err)
	require.Equal(t, 8*time.Hour, m)
	_, err = service.ParseListenMaxIdle("0")
	require.ErrorIs(t, err, service.ErrInvalidHubContact)
	_, err = service.ParseListenMaxIdle("100h")
	require.ErrorIs(t, err, service.ErrInvalidHubContact, "clamp may not exceed 72h")
}
