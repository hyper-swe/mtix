// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite_test

import (
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestImport_InteriorEventRetained_PreservesDestinationAck(t *testing.T) {
	for _, cumulative := range []bool{false, true} {
		t.Run(map[bool]string{false: "selective", true: "cumulative"}[cumulative], func(t *testing.T) {
			dest := newTestStore(t)
			first, _, last := addressThree(t, dest, "recipient")
			incoming := withoutExportNode(transportExport(t, dest), "PROJ-1")
			require.NoError(t, sqlite.RecomputeExportChecksum(incoming))
			if cumulative {
				require.NoError(t, dest.InboxAckThrough(t.Context(), "recipient", last))
			} else {
				require.NoError(t, dest.InboxAck(t.Context(), "recipient", first))
			}
			before, err := dest.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			_, err = dest.Import(t.Context(), incoming, sqlite.ImportModeReplace, false)
			require.NoError(t, err)
			after, err := dest.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			require.Len(t, after, len(before), "interior deletion also changes the restored event rowid")
		})
	}
}

func TestImportReconcile_AckAfterDryRun_PreservesNewerCoverage(t *testing.T) {
	dest := newTestStore(t)
	_, _, last := addressThree(t, dest, "recipient")
	incoming := withoutExportNode(transportExport(t, dest), "PROJ-3")
	require.NoError(t, sqlite.RecomputeExportChecksum(incoming))
	backups := 0
	_, _, err := dest.ImportReconcile(t.Context(), incoming, sqlite.ImportReconcileOptions{
		Mode:        sqlite.ImportModeReplace,
		BeforeWrite: func() error { backups++; return dest.InboxAck(t.Context(), "recipient", last) },
	})
	require.NoError(t, err)
	require.Equal(t, 1, backups)
	after, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, after, 2, "the real writer transaction must preserve a newer ack than the dry run saw")
}
