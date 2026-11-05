// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// FR-19.1: compare recovery with independent point reads beyond first/middle damage.
func TestRecover_DamagedInboxLeaf_KeepsEveryReadableRow(t *testing.T) {
	for _, position := range []string{"first", "middle"} {
		t.Run(position, func(t *testing.T) {
			source, raw := retentionStore(t)
			addressThree(t, source, "recipient")
			for i := range 60 {
				_, err := raw.Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES(?, 'PROJ','PROJ-1','comment',json_object('to','recipient','body',?),0,1,'{}','sender','','2026-11-05T00:00:00Z')`, fmt.Sprintf("leaf-mail-%03d", i), strings.Repeat("m", 1200))
				require.NoError(t, err)
			}
			path, root, size := transportBackupPage(t, source, "sync_events")
			db, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			var maxRow int64
			require.NoError(t, db.QueryRow(`SELECT MAX(rowid) FROM sync_events`).Scan(&maxRow))
			require.NoError(t, db.Close())
			damageInboxLeaf(t, path, root, size, position)
			expected, unreadable := readableInboxIDs(t, path, maxRow)
			require.Greater(t, len(expected), 40)
			require.Positive(t, unreadable)
			result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			require.NoError(t, err)
			require.NotNil(t, result.Export.InboxJournal)
			var got []string
			for _, event := range result.Export.InboxJournal.Events {
				got = append(got, event.EventID)
			}
			require.ElementsMatch(t, expected, got)
			require.Contains(t, strings.Join(result.Notes, " "), fmt.Sprintf("%d unreadable sync event record(s)", unreadable))
			require.NoError(t, sqlite.ValidateExport(result.Export))
		})
	}
}

func damageInboxLeaf(t *testing.T, path string, root, size int, position string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0600)
	require.NoError(t, err)
	page := make([]byte, size)
	_, err = file.ReadAt(page, int64((root-1)*size))
	require.NoError(t, err)
	require.Equal(t, byte(5), page[0])
	count := int(binary.BigEndian.Uint16(page[3:5]))
	require.Greater(t, count, 3)
	index := 0
	if position == "middle" {
		index = count / 2
	}
	offset := int(binary.BigEndian.Uint16(page[12+2*index:]))
	leaf := binary.BigEndian.Uint32(page[offset:])
	_, err = file.WriteAt(bytes.Repeat([]byte{0xff}, size), int64(leaf-1)*int64(size))
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

func readableInboxIDs(t *testing.T, path string, maxRow int64) ([]string, int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var ids []string
	failed := 0
	for seq := int64(1); seq <= maxRow; seq++ {
		var id, op string
		err := db.QueryRow(`SELECT event_id,op_type FROM sync_events NOT INDEXED WHERE rowid=?`, seq).Scan(&id, &op)
		if err != nil {
			failed++
			continue
		}
		if op == "comment" {
			ids = append(ids, id)
		}
	}
	return ids, failed
}
