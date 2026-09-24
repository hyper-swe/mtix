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
// in the table and an empty string in JSON.
func TestRunSyncStatus_LastSweep_ShownInTableAndJSON(t *testing.T) {
	tests := []struct {
		name      string
		stored    string
		wantJSON  string
		wantTable *regexp.Regexp
	}{
		{"never swept", "", "",
			regexp.MustCompile(`(?m)^last sweep\s+never$`)},
		{"swept", "2026-09-24T01:02:03.456789Z", "2026-09-24T01:02:03.456789Z",
			regexp.MustCompile(`(?m)^last sweep\s+2026-09-24T01:02:03\.456789Z$`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			_, err := app.store.WriteDB().ExecContext(ctx,
				`UPDATE meta SET value = ? WHERE key = 'meta.sync.last_sweep_at'`, tt.stored)
			require.NoError(t, err)

			var out, errOut bytes.Buffer
			app.jsonOutput = true
			require.NoError(t, runSyncStatus(ctx, &out, &errOut))
			var got map[string]any
			require.NoError(t, json.Unmarshal(out.Bytes(), &got))
			require.Contains(t, got, "last_sweep_at")
			require.Equal(t, tt.wantJSON, got["last_sweep_at"])

			out.Reset()
			app.jsonOutput = false
			require.NoError(t, runSyncStatus(ctx, &out, &errOut))
			require.Regexp(t, tt.wantTable, out.String())
		})
	}
}
