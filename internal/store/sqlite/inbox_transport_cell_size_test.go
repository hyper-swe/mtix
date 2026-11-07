// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// FR-19.1: one invalid cell pointer must not poison the rest of its page during salvage.
func TestRecover_BadInboxCellSize_KeepsOtherCells(t *testing.T) {
	source, raw := retentionStore(t)
	first, _, _ := addressThree(t, source, "recipient")
	// Make the first point read addressed mail: a page check may fail once and
	// then leave the page cached, so an unrelated first row would mask mail loss.
	_, err := raw.Exec(`DELETE FROM sync_events WHERE op_type != 'comment'`)
	require.NoError(t, err)
	path, root, size := transportBackupPage(t, source, "sync_events")
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	require.NoError(t, err)
	page := make([]byte, size)
	_, err = f.ReadAt(page, int64((root-1)*size))
	require.NoError(t, err)
	require.Equal(t, byte(13), page[0], "single leaf fixture")
	n := int(binary.BigEndian.Uint16(page[3:5]))
	require.Equal(t, 3, n)
	binary.BigEndian.PutUint16(page[8+2*(n-1):], 1)
	_, err = f.WriteAt(page, int64((root-1)*size))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	var readableID string
	for _, check := range []string{"0", "1"} {
		db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=cell_size_check("+check+")")
		require.NoError(t, err)
		var id string
		err = db.QueryRow(`SELECT event_id FROM sync_events NOT INDEXED WHERE rowid=?`, first).Scan(&id)
		if check == "0" {
			require.NoError(t, err)
			require.NotEmpty(t, id)
			readableID = id
		} else {
			require.Error(t, err, "forced check must reject the otherwise readable page")
		}
		require.NoError(t, db.Close())
	}
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.NotNil(t, result.Export.InboxJournal)
	var recoveredIDs []string
	for _, event := range result.Export.InboxJournal.Events {
		recoveredIDs = append(recoveredIDs, event.EventID)
	}
	require.Contains(t, recoveredIDs, readableID, "the proven readable first cell must survive salvage")
	require.NoError(t, sqlite.ValidateExport(result.Export))
}
