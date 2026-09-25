// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// quarantineN stores n quarantined events in the peer store.
func quarantineN(t *testing.T, n int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		for i := 0; i < n; i++ {
			if err := sqlite.QuarantineEvent(ctx, tx, sqlite.QuarantinedEvent{
				EventID: fmt.Sprintf("0193fb00-0000-7000-8000-00000000000%d", i), Source: "pull",
				RawEvent: `{"lamport_clock":1}`, Reason: "r",
				FirstSeen: "2026-09-25T00:00:00Z", LastAttempt: "2026-09-25T00:00:00Z",
			}); err != nil {
				return err
			}
		}
		return nil
	}))
}

// TestRunSyncStatus_Quarantined_ShownInTableAndJSON: `mtix sync status`
// reports how many pulled events are quarantined (MTIX-95.11 acceptance 5),
// as the quarantined_events JSON field and a "quarantined events" table
// row, zero included.
func TestRunSyncStatus_Quarantined_ShownInTableAndJSON(t *testing.T) {
	for _, n := range []int{0, 3} {
		t.Run(fmt.Sprintf("%d quarantined", n), func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			quarantineN(t, n)

			var out, errOut bytes.Buffer
			app.jsonOutput = true
			require.NoError(t, runSyncStatus(ctx, &out, &errOut))
			var got map[string]any
			require.NoError(t, json.Unmarshal(out.Bytes(), &got))
			require.Contains(t, got, "quarantined_events")
			require.Equal(t, float64(n), got["quarantined_events"])

			out.Reset()
			app.jsonOutput = false
			require.NoError(t, runSyncStatus(ctx, &out, &errOut))
			require.Regexp(t, regexp.MustCompile(fmt.Sprintf(`(?m)^quarantined events\s+%d$`, n)), out.String())
		})
	}
}
