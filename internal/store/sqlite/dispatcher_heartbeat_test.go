// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func openHeartbeatStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.New(filepath.Join(t.TempDir(), "store.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func TestListDispatcherHeartbeats_Empty_ReturnsNone(t *testing.T) {
	rows, err := openHeartbeatStore(t).ListDispatcherHeartbeats(t.Context(), "host-a", "501")
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestRecordDispatcherHeartbeat_Upsert_ListsLatestForHostAndUser(t *testing.T) {
	s := openHeartbeatStore(t)
	first := sqlite.DispatcherHeartbeat{HostID: "host-a", UID: "501", PID: 10, Protocol: 1, Version: "0.6.0", SeenAt: "2026-10-15T09:00:00Z"}
	require.NoError(t, s.RecordDispatcherHeartbeat(t.Context(), first))
	second := first
	second.PID, second.Protocol, second.Version, second.SeenAt = 11, 2, "0.6.1", "2026-10-15T09:05:00Z"
	require.NoError(t, s.RecordDispatcherHeartbeat(t.Context(), second))
	other := sqlite.DispatcherHeartbeat{HostID: "host-b", UID: "501", PID: 12, Protocol: 1, Version: "0.6.0", SeenAt: "2026-10-15T09:00:00Z"}
	require.NoError(t, s.RecordDispatcherHeartbeat(t.Context(), other))

	rows, err := s.ListDispatcherHeartbeats(t.Context(), "host-a", "501")
	require.NoError(t, err)
	assert.Equal(t, []sqlite.DispatcherHeartbeat{second}, rows)
}

func TestRecordDispatcherHeartbeat_InvalidInput_Refused(t *testing.T) {
	s := openHeartbeatStore(t)
	valid := sqlite.DispatcherHeartbeat{HostID: "h", UID: "1", PID: 1, Protocol: 1, Version: "v", SeenAt: "2026-10-15T09:00:00Z"}
	tests := []struct {
		name   string
		mutate func(*sqlite.DispatcherHeartbeat)
	}{
		{"empty host", func(h *sqlite.DispatcherHeartbeat) { h.HostID = "" }},
		{"empty uid", func(h *sqlite.DispatcherHeartbeat) { h.UID = "" }},
		{"zero pid", func(h *sqlite.DispatcherHeartbeat) { h.PID = 0 }},
		{"negative protocol", func(h *sqlite.DispatcherHeartbeat) { h.Protocol = -1 }},
		{"empty seen time", func(h *sqlite.DispatcherHeartbeat) { h.SeenAt = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hb := valid
			tt.mutate(&hb)
			assert.Error(t, s.RecordDispatcherHeartbeat(t.Context(), hb))
		})
	}
}
