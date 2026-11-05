// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// FR-19.1 / MTIX-112.2.1: a damaged later leaf must not discard a readable prefix.
func TestRecover_DamagedLaterJournalPage_PreservesReadablePrefix(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	for i := range 30 {
		_, err := raw.Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at)
 VALUES(?, 'PROJ','PROJ-1','comment',json_object('to','recipient','body',?),0,1,'{}','sender','','2026-11-05T00:00:00Z')`, strings.Repeat("x", i+1), strings.Repeat("m", 1200))
		require.NoError(t, err)
	}
	path, root, size := transportBackupPage(t, source, "sync_events")
	file, err := os.OpenFile(path, os.O_RDWR, 0600)
	require.NoError(t, err)
	header := make([]byte, 12)
	_, err = file.ReadAt(header, int64((root-1)*size))
	require.NoError(t, err)
	require.Equal(t, byte(5), header[0], "fixture must have an interior table page")
	lastPage := binary.BigEndian.Uint32(header[8:12])
	_, err = file.WriteAt(bytes.Repeat([]byte{0xff}, size), int64(lastPage-1)*int64(size))
	require.NoError(t, err)
	require.NoError(t, file.Close())
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "missing.json"), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.NotNil(t, result.Export.InboxJournal)
	require.NotEmpty(t, result.Export.InboxJournal.Events)
	require.Less(t, len(result.Export.InboxJournal.Events), 33)
	require.Contains(t, strings.Join(result.Notes, " "), "partially unreadable")
	require.NoError(t, sqlite.ValidateExport(result.Export))
	dest := newTestStore(t)
	_, err = dest.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	got, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, got, len(result.Export.InboxJournal.Events))
}
