// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// FR-19.1: collision and retirement failures leave restored mail and clocks intact.
func TestRestoredInbox_UpgradeFailureRollsBack(t *testing.T) {
	for _, fault := range []string{"collision", "retire"} {
		t.Run(fault, func(t *testing.T) {
			source := newTestStore(t)
			addressThree(t, source, "recipient")
			dest, raw := retentionStore(t)
			file := transportExport(t, source)
			_, err := dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
			require.NoError(t, err)
			events, err := source.ReadPendingEvents(t.Context(), 100)
			require.NoError(t, err)
			var event *model.SyncEvent
			for _, e := range events {
				if e.OpType == model.OpComment {
					event = e
					break
				}
			}
			require.NotNil(t, event)
			before, err := dest.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			if fault == "collision" {
				event.Payload = json.RawMessage(`{"to":"recipient","body":"different"}`)
			} else {
				_, err = raw.Exec(`CREATE TRIGGER reject_retirement BEFORE DELETE ON inbox_restored_events BEGIN SELECT RAISE(ABORT,'retirement failed'); END`)
				require.NoError(t, err)
			}
			err = dest.WithTx(t.Context(), func(tx *sql.Tx) error { return dest.IdempotentApply(t.Context(), tx, event) })
			require.Error(t, err)
			after, err := dest.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			require.Equal(t, before, after)
			var clock, marked, applied int
			require.NoError(t, raw.QueryRow(`SELECT lamport_clock FROM sync_events WHERE event_id=?`, event.EventID).Scan(&clock))
			require.Zero(t, clock)
			require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM inbox_restored_events`).Scan(&marked))
			require.Equal(t, 3, marked)
			require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM applied_events`).Scan(&applied))
			require.Zero(t, applied)
		})
	}
}
