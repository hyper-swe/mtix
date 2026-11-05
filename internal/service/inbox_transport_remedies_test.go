// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAutoImport_RestoredInboxRefusalAndPendingNoticeStaySafe(t *testing.T) {
	f := newGuardFixture(t)
	markFixtureSynced(t, f)
	_, err := f.store.WriteDB().Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,sync_status,created_at) VALUES('restored-safety-mail','PROJ','PROJ-1','comment','{"to":"worker","body":"private"}',0,0,'{}','sender','','applied','2026-11-05T00:00:00Z')`)
	require.NoError(t, err)
	_, err = f.store.WriteDB().Exec(`INSERT INTO inbox_restored_events VALUES('restored-safety-mail')`)
	require.NoError(t, err)
	board := f.teammateBoard(t, func(d *sqlite.ExportData) { d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations = nil })
	f.pull(t, board)
	require.ErrorIs(t, f.svc.AutoImport(t.Context(), f.mtixDir), service.ErrAutoImportRefused)
	require.Contains(t, f.notices.String(), "restored mail")
	require.NotContains(t, f.notices.String(), "--discard-local")
	f.notices.Reset()
	writeLocalTask(t, f, "PROJ-4", 4)
	require.NoError(t, f.svc.AutoExport(t.Context(), f.mtixDir))
	require.Contains(t, f.notices.String(), "new empty directory")
	require.NotContains(t, f.notices.String(), "--discard-local")
	mail, err := f.store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, mail, 1)
}
