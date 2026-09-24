// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunSyncStatus_LastSweep_ShownInTableAndJSON: `mtix sync status`
// reports meta.sync.last_sweep_at, the hub time of the last late-event
// sweep (MTIX-95.5 acceptance 6), both as a table row and as the
// last_sweep_at JSON field. A store that has never swept shows "never"
// in the table and an empty string in JSON; while the first full
// comparison is part-way done (saved listing progress, or staged ids not
// yet applied), the table says so and JSON sets full_sweep_in_progress.
// JSON also counts the staged ids (sweep_pending_events).
func TestRunSyncStatus_LastSweep_ShownInTableAndJSON(t *testing.T) {
	tests := []struct {
		name         string
		stored       string
		afterID      string
		startedAt    string
		staged       []string
		wantJSON     string
		wantProgress bool
		wantTable    *regexp.Regexp
	}{
		{"never swept", "", "", "", nil, "", false,
			regexp.MustCompile(`(?m)^last sweep\s+never$`)},
		{"swept", "2026-09-24T01:02:03.456789Z", "", "", nil, "2026-09-24T01:02:03.456789Z", false,
			regexp.MustCompile(`(?m)^last sweep\s+2026-09-24T01:02:03\.456789Z$`)},
		{"full comparison listing in progress", "", "0193fb00-0000-7000-8000-000000000007",
			"2026-09-24T01:00:00Z", []string{"0193fb00-0000-7000-8000-000000000003"}, "", true,
			regexp.MustCompile(`(?m)^last sweep\s+never \(full hub comparison in progress\)$`)},
		{"full comparison with staged ids only", "", "", "",
			[]string{"0193fb00-0000-7000-8000-000000000003", "0193fb00-0000-7000-8000-000000000004"}, "", true,
			regexp.MustCompile(`(?m)^last sweep\s+never \(full hub comparison in progress\)$`)},
		{"windowed sweep in progress", "2026-09-24T01:02:03Z", "0193fb00-0000-7000-8000-000000000007",
			"2026-09-24T01:20:00Z", nil, "2026-09-24T01:02:03Z", false,
			regexp.MustCompile(`(?m)^last sweep\s+2026-09-24T01:02:03Z$`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			for key, v := range map[string]string{
				"meta.sync.last_sweep_at":    tt.stored,
				"meta.sync.sweep_after_id":   tt.afterID,
				"meta.sync.sweep_started_at": tt.startedAt,
			} {
				_, err := app.store.WriteDB().ExecContext(ctx,
					`UPDATE meta SET value = ? WHERE key = ?`, v, key)
				require.NoError(t, err)
			}
			for _, id := range tt.staged {
				_, err := app.store.WriteDB().ExecContext(ctx,
					`INSERT INTO sync_sweep_pending (event_id) VALUES (?)`, id)
				require.NoError(t, err)
			}

			var out, errOut bytes.Buffer
			app.jsonOutput = true
			require.NoError(t, runSyncStatus(ctx, &out, &errOut))
			var got map[string]any
			require.NoError(t, json.Unmarshal(out.Bytes(), &got))
			require.Contains(t, got, "last_sweep_at")
			require.Equal(t, tt.wantJSON, got["last_sweep_at"])
			require.Equal(t, tt.wantProgress, got["full_sweep_in_progress"])
			require.Equal(t, float64(len(tt.staged)), got["sweep_pending_events"])

			out.Reset()
			app.jsonOutput = false
			require.NoError(t, runSyncStatus(ctx, &out, &errOut))
			require.Regexp(t, tt.wantTable, out.String())
		})
	}
}
