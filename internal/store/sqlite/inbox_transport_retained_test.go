// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestImport_ReplaceRetainsJournalIdentity_PreservesLocalBookkeeping(t *testing.T) {
	for _, node := range []string{"PROJ-1", "PROJ-3"} {
		t.Run(node, func(t *testing.T) {
			dest, raw := retentionStore(t)
			addressThree(t, dest, "recipient")
			mail, err := dest.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			var selected sqlite.InboxEvent
			for _, event := range mail {
				if event.NodeID == node {
					selected = event
				}
			}
			require.NotEmpty(t, selected.EventID)
			require.NoError(t, dest.RecordInboxDelivery(t.Context(), "hooked", selected.Seq, "local-hook"))
			pending, err := dest.ReadPendingEvents(t.Context(), 100)
			require.NoError(t, err)
			var held *model.SyncEvent
			for _, event := range pending {
				if event.EventID == selected.EventID {
					held = event
				}
			}
			require.NotNil(t, held)
			incoming := withoutExportNode(transportExport(t, dest), node)
			require.NoError(t, sqlite.RecomputeExportChecksum(incoming))
			_, err = dest.Import(t.Context(), incoming, sqlite.ImportModeReplace, false)
			require.NoError(t, err)
			deliveries, err := dest.InboxList(t.Context(), "hooked")
			require.NoError(t, err)
			require.Len(t, deliveries, 1, "existing local hook delivery must survive")
			require.Equal(t, selected.Seq, deliveries[0].Seq)
			pending, err = dest.ReadPendingEvents(t.Context(), 100)
			require.NoError(t, err)
			var retained *model.SyncEvent
			for _, event := range pending {
				if event.EventID == selected.EventID {
					retained = event
				}
			}
			require.Nil(t, retained, "mail for a dropped task must leave the outbound queue")
			var status string
			var lamport, wall, seq int64
			require.NoError(t, raw.QueryRow(`SELECT sync_status,lamport_clock,wall_clock_ts,rowid FROM sync_events WHERE event_id=?`, selected.EventID).Scan(&status, &lamport, &wall, &seq))
			require.Equal(t, "applied", status)
			require.Equal(t, held.LamportClock, lamport)
			require.Equal(t, held.WallClockTS, wall)
			require.Equal(t, selected.Seq, seq)
			hooks, err := dest.ReadJournalSince(t.Context(), 0, 100)
			require.NoError(t, err)
			present := false
			for _, hook := range hooks {
				present = present || hook.EventID == selected.EventID
			}
			require.True(t, present, "unprocessed local hook must not be suppressed as a restore")
		})
	}
}
