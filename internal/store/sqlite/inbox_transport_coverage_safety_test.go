// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
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

func setDamageCoverage(t *testing.T, path string, cursor, ack int64) (int, int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	if cursor > 0 {
		_, err = db.Exec(`INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES('recipient',?)`, cursor)
		require.NoError(t, err)
	}
	if ack > 0 {
		_, err = db.Exec(`INSERT INTO agent_inbox_ack(agent_id,event_seq,acked_at) VALUES('recipient',?,'2026-11-05T00:00:00Z')`, ack)
		require.NoError(t, err)
	}
	var root, size int
	require.NoError(t, db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name='sqlite_autoindex_agent_inbox_ack_1'`).Scan(&root))
	require.NoError(t, db.QueryRow(`PRAGMA page_size`).Scan(&size))
	return root, size
}

// Patch the integer key in a one-cell SQLite index leaf, without modifying the
// acknowledgment table. This parser is a fixture helper, never salvage code.
func flipAckIndexKey(t *testing.T, data []byte, root, size int, from, to int64) {
	t.Helper()
	page := (root - 1) * size
	require.Equal(t, byte(10), data[page])
	require.Equal(t, uint16(1), binary.BigEndian.Uint16(data[page+3:]))
	cell := page + int(binary.BigEndian.Uint16(data[page+8:]))
	_, prefix := rowVarint(data[cell:])
	record := cell + prefix
	header, n := rowVarint(data[record:])
	textType, width := rowVarint(data[record+n:])
	seqType, _ := rowVarint(data[record+n+width:])
	require.Equal(t, int64(2), seqType, "fixture uses a two-byte integer key")
	at := record + int(header) + int((textType-13)/2)
	require.Equal(t, from, int64(binary.BigEndian.Uint16(data[at:])))
	mask := uint16(from ^ to)
	require.NotZero(t, mask)
	require.Zero(t, mask&(mask-1), "fixture changes exactly one bit")
	binary.BigEndian.PutUint16(data[at:], uint16(to))
}

func requireAllRecoveredUnread(t *testing.T, result *sqlite.RecoverResult, target string, count int) {
	t.Helper()
	require.True(t, result.InboxIncomplete)
	journal := result.Export.InboxJournal
	require.Len(t, journal.Events, count)
	dest, _ := retentionStore(t)
	_, err := dest.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	mail, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	var ids []string
	for _, event := range mail {
		ids = append(ids, event.EventID)
	}
	require.Contains(t, ids, target, "the named unread mail must remain visible after fresh import")
	require.Len(t, mail, count)
	for _, event := range journal.Events {
		require.False(t, event.Acked, "incomplete salvage must not retain coverage for %s", event.EventID)
		require.Empty(t, event.AckedAt, "incomplete salvage must clear acknowledgment time")
	}
	notes := strings.Join(result.Notes, " ")
	require.Contains(t, notes, fmt.Sprintf("all %d will be delivered as unread", count))
	require.Contains(t, notes, "repeated wakes")
}

// FR-19.1 / MTIX-112.48: recovery coverage follows the whole-store integrity verdict.
func TestRecover_DamagedCoverage_AllMailRemainsUnread(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		from, to, cursor, ack int64
		index                 bool
	}{
		{"neighbor-150-to-151", 150, 151, 0, 151, false},
		{"neighbor-249-to-251", 249, 251, 0, 251, false},
		{"cursor-111-to-110", 111, 110, 110, 0, false},
		{"ack-index-251-to-250", 251, 250, 0, 251, true},
		{"ack-index-152-to-144", 152, 144, 0, 152, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, root, size := damageMailFixture(t, "")
			indexRoot, indexSize := setDamageCoverage(t, path, tc.cursor, tc.ack)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			target := tc.from
			if tc.index {
				flipAckIndexKey(t, data, indexRoot, indexSize, tc.from, tc.to)
				target = tc.to
			} else {
				patched := false
				for _, cell := range rowCells(t, data, root, size) {
					if cell.seq != tc.from {
						continue
					}
					mask := tc.from ^ tc.to
					require.Zero(t, mask&(mask-1))
					require.Less(t, mask, int64(128))
					data[cell.at+cell.length-1] ^= byte(mask)
					got, _ := rowVarint(data[cell.at:])
					require.Equal(t, tc.to, got)
					patched = true
				}
				require.True(t, patched)
			}
			require.NoError(t, os.WriteFile(path, data, 0600))
			result := recoverDamageMail(t, path)
			requireAllRecoveredUnread(t, result, fmt.Sprintf("damage-%03d", target), 300)
		})
	}
}

// FR-19.1 / MTIX-112.48: recovery coverage follows the whole-store integrity verdict.
func TestRecover_IncompleteSource_DiscardsEvenValidExportCoverage(t *testing.T) {
	for _, mode := range []string{"rejected", "conflicting"} {
		t.Run(mode, func(t *testing.T) {
			source, raw := retentionStore(t)
			first, _, _ := addressThree(t, source, "recipient")
			require.NoError(t, source.InboxAck(t.Context(), "recipient", first))
			file := transportExport(t, source)
			supplied := []*sqlite.ExportData{file}
			if mode == "rejected" {
				supplied = append([]*sqlite.ExportData{{InboxJournal: &sqlite.InboxJournal{Version: 2}}}, supplied...)
			} else {
				_, err := raw.Exec(`UPDATE sync_events SET payload=json_set(payload,'$.body','changed source content') WHERE event_id=?`, file.InboxJournal.Events[0].EventID)
				require.NoError(t, err)
			}
			var path string
			require.NoError(t, raw.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path))
			result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), InboxSources: supplied})
			require.NoError(t, err)
			require.NoError(t, sqlite.ValidateExport(result.Export))
			requireAllRecoveredUnread(t, result, file.InboxJournal.Events[0].EventID, 3)
		})
	}
}

// FR-19.1 / MTIX-112.48: recovery coverage follows the whole-store integrity verdict.
func TestRecover_UnverifiableAck_DiscardsOtherCoverage(t *testing.T) {
	source, raw := retentionStore(t)
	first, second, third := addressThree(t, source, "recipient")
	for _, seq := range []int64{first, second, third} {
		require.NoError(t, source.InboxAck(t.Context(), "recipient", seq))
	}
	var firstID string
	require.NoError(t, raw.QueryRow(`SELECT event_id FROM sync_events WHERE rowid=?`, first).Scan(&firstID))
	_, err := raw.Exec(`UPDATE agent_inbox_ack SET acked_at='invalid' WHERE event_seq=?`, first)
	require.NoError(t, err)
	var path string
	require.NoError(t, raw.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path))
	result := recoverDamageMail(t, path)
	requireAllRecoveredUnread(t, result, firstID, 3)
}

// FR-19.1 / MTIX-112.48: recovery coverage follows the whole-store integrity verdict.
func TestRecover_CleanDatabase_RetainsSelectiveAndCursorCoverage(t *testing.T) {
	source, raw := retentionStore(t)
	first, second, third := addressThree(t, source, "recipient")
	require.NoError(t, source.InboxAckThrough(t.Context(), "recipient", first))
	require.NoError(t, source.InboxAck(t.Context(), "recipient", third))
	const stamp = "2026-11-05T01:02:03Z"
	_, err := raw.Exec(`UPDATE agent_inbox_ack SET acked_at=? WHERE event_seq=?`, stamp, third)
	require.NoError(t, err)
	var path, secondID string
	require.NoError(t, raw.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path))
	require.NoError(t, raw.QueryRow(`SELECT event_id FROM sync_events WHERE rowid=?`, second).Scan(&secondID))
	result := recoverDamageMail(t, path)
	require.False(t, result.InboxIncomplete)
	events := result.Export.InboxJournal.Events
	require.Len(t, events, 3)
	require.True(t, events[0].Acked)
	require.Empty(t, events[0].AckedAt, "cursor coverage must not invent a timestamp")
	require.False(t, events[1].Acked)
	require.True(t, events[2].Acked)
	require.Equal(t, stamp, events[2].AckedAt)
	dest, _ := retentionStore(t)
	_, err = dest.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	mail, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, mail, 1)
	require.Equal(t, secondID, mail[0].EventID)
}
