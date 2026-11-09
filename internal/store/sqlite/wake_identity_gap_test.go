// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWakeIdentity_VacantSequence_RejectsNextRelocatedID(t *testing.T) {
	for _, operation := range []string{"claim", "accounted claim", "hold", "advance"} {
		t.Run(operation, func(t *testing.T) {
			f := newWakeReplaceState(t, "above", false)
			key := replayWakeIdentityElsewhere(t, f)
			// Pin strict sequence lookup even when a valid journal has a gap. All
			// events and wake state above were created by real replace/replay APIs;
			// only remove the intervening journal rows, without forging identities.
			_, err := f.raw.Exec(`DELETE FROM sync_events WHERE rowid>=? AND rowid<
    (SELECT rowid FROM sync_events WHERE event_id=?)`, key.EventSeq, key.EventID)
			require.NoError(t, err)
			var count, relocated int64
			require.NoError(t, f.raw.QueryRow(`SELECT COUNT(*) FROM sync_events WHERE rowid=?`, key.EventSeq).Scan(&count))
			require.Zero(t, count, "the old journal sequence is vacant")
			var nextID string
			require.NoError(t, f.raw.QueryRow(`SELECT rowid,event_id FROM sync_events WHERE rowid>=? ORDER BY rowid LIMIT 1`, key.EventSeq).Scan(&relocated, &nextID))
			require.Greater(t, relocated, key.EventSeq)
			require.Equal(t, key.EventID, nextID, "the next row is precisely the relocated snapshot identity")
			before := wakeIdentityRows(t, f)
			require.NoError(t, refusedWakeOperation(t, f.store, key, operation))
			assert.Equal(t, before, wakeIdentityRows(t, f))
		})
	}
}
