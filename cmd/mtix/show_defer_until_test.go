// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-107.75: show renders a wake time only for a timed deferred node,
// in UTC, while leaving the stored record and JSON representation unchanged.
func TestRunShow_DeferUntil_PrintsOnlyDeferredWakeTime(t *testing.T) {
	tests := []struct {
		name   string
		status model.Status
		until  any
		want   string
	}{
		{"timed deferred", model.StatusDeferred, "2026-10-01T09:00:00Z", "Status:   ⏸ deferred (until 2026-10-01T09:00:00Z)"},
		{"indefinite deferred", model.StatusDeferred, nil, "Status:   ⏸ deferred"},
		{"stale open wake time", model.StatusOpen, "2026-10-01T09:00:00Z", "Status:   ○ open"},
		{"open without wake time", model.StatusOpen, nil, "Status:   ○ open"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			id := createShowNode(t, "")
			// Deliberately preserve stale wake times to exercise imported legacy data.
			_, err := app.store.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET status = ?, defer_until = ? WHERE id = ?`, tt.status, tt.until, id)
			require.NoError(t, err)
			out := showOutput(t, id)
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "Status:") {
					assert.Equal(t, tt.want, line)
				}
			}
			assert.Contains(t, out, tt.want+"\n")
			node, err := app.store.GetNode(context.Background(), id)
			require.NoError(t, err)
			wantJSON, err := json.Marshal(node)
			require.NoError(t, err)
			app.jsonOutput = true
			gotJSON := captureStdout(t, func() { require.NoError(t, runShow(id)) })
			assert.JSONEq(t, string(wantJSON), gotJSON)
		})
	}
}

func TestShowStatus_DeferUntil_NormalizesUTC(t *testing.T) {
	tests := []struct {
		name   string
		status model.Status
		until  string
		want   string
	}{
		{"utc", model.StatusDeferred, "2026-10-01T09:00:00Z", "⏸ deferred (until 2026-10-01T09:00:00Z)"},
		{"positive offset crosses date", model.StatusDeferred, "2026-10-02T00:30:00+05:30", "⏸ deferred (until 2026-10-01T19:00:00Z)"},
		{"negative offset", model.StatusDeferred, "2026-10-01T04:00:00-05:00", "⏸ deferred (until 2026-10-01T09:00:00Z)"},
		{"missing", model.StatusDeferred, "", "⏸ deferred"},
		{"stale nondeferred", model.StatusDone, "2026-10-01T09:00:00Z", "✓ done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &model.Node{Status: tt.status}
			if tt.until != "" {
				until, err := time.Parse(time.RFC3339, tt.until)
				require.NoError(t, err)
				node.DeferUntil = &until
			}
			before, err := json.Marshal(node)
			require.NoError(t, err)
			assert.Equal(t, tt.want, showStatus(node))
			after, err := json.Marshal(node)
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after), "rendering must not mutate the record")
		})
	}
}
