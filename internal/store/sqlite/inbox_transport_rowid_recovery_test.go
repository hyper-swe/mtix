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

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// FR-19.1: a readable but incomplete identity index is not a recovery boundary.
func TestRecover_ShortIdentityIndex_PreservesAllReadableMail(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(fmt.Sprint(zero), func(t *testing.T) {
			source, raw := retentionStore(t)
			addressThree(t, source, "recipient")
			path, root, size := transportBackupPage(t, source, "sqlite_autoindex_sync_events_1")
			old, err := os.ReadFile(path)
			require.NoError(t, err)
			indexPage := append([]byte(nil), old[(root-1)*size:root*size]...)
			require.Equal(t, byte(10), indexPage[0])
			for i := range 3 {
				_, err = raw.Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES(?, 'PROJ','PROJ-1','comment','{"to":"recipient","body":"later"}',0,1,'{}','sender','','2026-11-05T00:00:00Z')`, fmt.Sprintf("later-%d", i))
				require.NoError(t, err)
			}
			path, root, size = transportBackupPage(t, source, "sqlite_autoindex_sync_events_1")
			if zero {
				binary.BigEndian.PutUint16(indexPage[3:5], 0)
			}
			f, err := os.OpenFile(path, os.O_RDWR, 0600)
			require.NoError(t, err)
			_, err = f.WriteAt(indexPage, int64((root-1)*size))
			require.NoError(t, err)
			require.NoError(t, f.Close())
			result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			require.NoError(t, err)
			require.NotNil(t, result.Export.InboxJournal)
			require.Len(t, result.Export.InboxJournal.Events, 6)
		})
	}
}

// FR-19.1: damage to the identity index cannot hide readable rows past a bad table page.
func TestRecover_IdentityIndexAndFirstLeafDamaged_PreservesReadableMail(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	for i := range 60 {
		_, err := raw.Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES(?, 'PROJ','PROJ-1','comment',json_object('to','recipient','body',?),0,1,'{}','sender','','2026-11-05T00:00:00Z')`, fmt.Sprintf("both-%03d", i), strings.Repeat("m", 1200))
		require.NoError(t, err)
	}
	path, root, size := transportBackupPage(t, source, "sync_events")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	var indexRoot int
	var maxRow int64
	require.NoError(t, db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name='sqlite_autoindex_sync_events_1'`).Scan(&indexRoot))
	require.NoError(t, db.QueryRow(`SELECT MAX(rowid) FROM sync_events`).Scan(&maxRow))
	require.NoError(t, db.Close())
	damageInboxLeaf(t, path, root, size, "first")
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	require.NoError(t, err)
	_, err = f.WriteAt(bytes.Repeat([]byte{0xff}, size), int64(indexRoot-1)*int64(size))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	expected, unreadable := readableInboxIDs(t, path, maxRow)
	require.Greater(t, len(expected), 40)
	require.Positive(t, unreadable)
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.NotNil(t, result.Export.InboxJournal)
	var got []string
	for _, e := range result.Export.InboxJournal.Events {
		got = append(got, e.EventID)
	}
	require.ElementsMatch(t, expected, got)
}

func TestRecover_UnreadableTailAndEarlierLeaf_PreservesMiddleAndReportsLimit(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	for i := range 60 {
		_, err := raw.Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES(?, 'PROJ','PROJ-1','comment',json_object('to','recipient','body',?),0,1,'{}','sender','','2026-11-05T00:00:00Z')`, fmt.Sprintf("both-%03d", i), strings.Repeat("m", 1200))
		require.NoError(t, err)
	}
	path, root, size := transportBackupPage(t, source, "sync_events")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	var indexRoot int
	var maxRow int64
	require.NoError(t, db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name='sqlite_autoindex_sync_events_1'`).Scan(&indexRoot))
	require.NoError(t, db.QueryRow(`SELECT MAX(rowid) FROM sync_events`).Scan(&maxRow))
	require.NoError(t, db.Close())
	damageInboxLeaf(t, path, root, size, "first")
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	require.NoError(t, err)
	_, err = f.WriteAt(bytes.Repeat([]byte{0xff}, size), int64(indexRoot-1)*int64(size))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	tailFile, err := os.OpenFile(path, os.O_RDWR, 0600)
	require.NoError(t, err)
	header := make([]byte, 12)
	_, err = tailFile.ReadAt(header, int64(root-1)*int64(size))
	require.NoError(t, err)
	last := binary.BigEndian.Uint32(header[8:12])
	_, err = tailFile.WriteAt(bytes.Repeat([]byte{0xff}, size), int64(last-1)*int64(size))
	require.NoError(t, err)
	require.NoError(t, tailFile.Close())
	expected, unreadable := readableInboxIDs(t, path, maxRow)
	require.Greater(t, len(expected), 40)
	require.Positive(t, unreadable)
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.NotNil(t, result.Export.InboxJournal)
	var got []string
	for _, e := range result.Export.InboxJournal.Events {
		got = append(got, e.EventID)
	}
	require.ElementsMatch(t, expected, got)
	require.True(t, result.InboxIncomplete)
	require.Contains(t, strings.Join(result.Notes, " "), "error-probe limit 10000 reached")
	require.Contains(t, strings.Join(result.Notes, " "), "lost record count unknown")
}
