// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.3: a restored ack whose source had compacted away its time (empty
// AckedAt) is covered but time-unknown. The restore invents neither a time nor
// a selective kind.
func TestImport_AckedEventWithEmptyAckedAt_ReportsWatermarkTimeUnknown(t *testing.T) {
	source := newTestStore(t)
	first, _, _ := addressThree(t, source, "recipient")
	require.NoError(t, source.InboxAckThrough(t.Context(), "recipient", first))
	file := transportExport(t, source)
	var acked string
	for i := range file.InboxJournal.Events {
		if file.InboxJournal.Events[i].Acked {
			file.InboxJournal.Events[i].AckedAt = ""
			acked = file.InboxJournal.Events[i].EventID
		}
	}
	require.NotEmpty(t, acked, "fixture must carry an acked event")
	destination := newTestStore(t)
	_, err := destination.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	hist, err := destination.InboxHistory(t.Context(), "recipient")
	require.NoError(t, err)
	var seq int64
	for _, h := range hist {
		if h.EventID == acked {
			seq = h.Seq
		}
	}
	require.NotZero(t, seq)
	info, err := destination.InboxAckInfo(t.Context(), "recipient", seq)
	require.NoError(t, err)
	require.Equal(t, sqlite.AckInfo{Kind: sqlite.AckWatermark}, info)
}
