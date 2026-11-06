// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type flippedRowCell struct {
	seq              int64
	at, length, leaf int
}

func rowVarint(b []byte) (int64, int) {
	var value uint64
	for i := 0; i < 8; i++ {
		value = value<<7 | uint64(b[i]&127)
		if b[i]&128 == 0 {
			return int64(value), i + 1
		}
	}
	return int64(value<<8 | uint64(b[8])), 9
}

// Only the test fixture parses pages; production recovery remains SQL-only.
func rowCells(t *testing.T, data []byte, root, size int) []flippedRowCell {
	t.Helper()
	start := (root - 1) * size
	header := start
	if root == 1 {
		header += 100
	}
	count := int(binary.BigEndian.Uint16(data[header+3:]))
	var cells []flippedRowCell
	switch data[header] {
	case 5:
		for i := range count {
			at := start + int(binary.BigEndian.Uint16(data[header+12+2*i:]))
			child := int(binary.BigEndian.Uint32(data[at:]))
			cells = append(cells, rowCells(t, data, child, size)...)
		}
		child := int(binary.BigEndian.Uint32(data[header+8:]))
		cells = append(cells, rowCells(t, data, child, size)...)
	case 13:
		for i := range count {
			at := start + int(binary.BigEndian.Uint16(data[header+8+2*i:]))
			_, n := rowVarint(data[at:])
			at += n
			seq, n := rowVarint(data[at:])
			cells = append(cells, flippedRowCell{seq, at, n, root})
		}
	default:
		t.Fatalf("unexpected page type %d", data[header])
	}
	return cells
}

func rowFlipFixture(t *testing.T) (string, int, int) {
	t.Helper()
	source, raw := retentionStore(t)
	addressTo(t, source, "recipient", "PROJ-1")
	_, err := raw.Exec(`DELETE FROM sync_events`)
	require.NoError(t, err)
	for seq := 1; seq <= 341; seq++ {
		payload := fmt.Sprintf(`{"body":%q}`, strings.Repeat("m", 100))
		if seq%2 == 0 {
			payload = fmt.Sprintf(`{"to":"recipient","body":%q}`, strings.Repeat("m", 100))
		}
		_, err = raw.Exec(`INSERT INTO sync_events(rowid,event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES(?,?,'PROJ','PROJ-1','comment',?,0,1,'{}','sender','','2026-11-05T00:00:00Z')`, seq, fmt.Sprintf("flip-%03d", seq), payload)
		require.NoError(t, err)
	}
	return transportBackupPage(t, source, "sync_events")
}

func scannedMailIDs(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	rows, err := db.Query(`SELECT event_id,payload FROM sync_events NOT INDEXED`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var ids []string
	for rows.Next() {
		var id, payload string
		require.NoError(t, rows.Scan(&id, &payload))
		if strings.Contains(payload, `"to":"recipient"`) {
			ids = append(ids, id)
		}
	}
	require.NoError(t, rows.Err())
	return ids
}

func recoverFlippedMail(t *testing.T, path string, expected []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result, err := sqlite.Recover(ctx, path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, ctx.Err(), "recovery must terminate before the deadline, without relying on cancellation")
	require.NoError(t, err)
	require.NotNil(t, result.Export.InboxJournal)
	var ids []string
	for _, event := range result.Export.InboxJournal.Events {
		ids = append(ids, event.EventID)
	}
	require.ElementsMatch(t, expected, ids, "every readable table-scan mail row survives")
	require.True(t, result.InboxIncomplete, "damaged ordering must not report complete salvage")
	require.Contains(t, strings.Join(result.Notes, " "), "lost record count unknown")
	require.NoError(t, sqlite.ValidateExport(result.Export))
}

func TestRecover_FlippedRowid8363_UnionsReadableScan(t *testing.T) {
	path, root, size := rowFlipFixture(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	found := false
	for _, cell := range rowCells(t, data, root, size) {
		if cell.seq != 171 {
			continue
		}
		require.Equal(t, 2, cell.length)
		data[cell.at] ^= 0x40
		value, _ := rowVarint(data[cell.at:])
		require.Equal(t, int64(8363), value)
		found = true
	}
	require.True(t, found)
	require.NoError(t, os.WriteFile(path, data, 0600))
	expected := scannedMailIDs(t, path)
	require.Len(t, expected, 170)
	recoverFlippedMail(t, path, expected)
}

func TestRecover_BackwardRowid_TerminatesAndKeepsReadableScan(t *testing.T) {
	path, root, size := rowFlipFixture(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	cells := rowCells(t, data, root, size)
	var target flippedRowCell
	for i, cell := range cells {
		if i > 0 && cell.leaf != cells[i-1].leaf && cell.seq >= 128 && cell.seq < 256 {
			target = cell
			break
		}
	}
	require.NotZero(t, target.seq)
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM sync_events WHERE rowid=?`, target.seq-1)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	before, _ := rowVarint(data[target.at:])
	require.Equal(t, target.seq, before)
	data[target.at] ^= 1
	after, _ := rowVarint(data[target.at:])
	require.Equal(t, target.seq-128, after)
	require.NoError(t, os.WriteFile(path, data, 0600))
	expected := scannedMailIDs(t, path)
	require.Greater(t, len(expected), 160)
	recoverFlippedMail(t, path, expected)
}
