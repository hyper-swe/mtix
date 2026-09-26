// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.3 in mtix recover (FR-26.5) under a damaged
// primary-key index: an index entry for one node id that points at another
// node's row. The fixture patches the entry's rowid in the database file,
// the damage a torn index page can leave. Written red-first.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// The comment the mirror holds for RC-2 in the damaged-index fixture.
const (
	rc2CommentID   = "01J9RCCOMMENT0000000000R2"
	rc2CommentText = "RC-2 only: a comment that belongs to another task"
)

// sqliteVarint decodes the SQLite varint at the start of b and returns its
// value and length in bytes.
func sqliteVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < 8; i++ {
		v = v<<7 | uint64(b[i]&0x7f)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return v<<8 | uint64(b[8]), 9
}

// repointIndexEntries makes the primary-key index entry of each node id in
// repoint, in the closed database at dbPath, point at the row of the node it
// maps to (MTIX-95.31.3). It folds the write-ahead log into the file and
// looks every rowid up before patching anything, then rewrites the rowid
// byte of each entry in the index's root page, which must be a leaf page (a
// small store); every rowid involved must fit in one byte, and each entry
// patched must store its rowid in one byte (a rowid above 1).
func repointIndexEntries(t *testing.T, dbPath string, repoint map[string]string) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `PRAGMA journal_mode = DELETE`)
	require.NoError(t, err)
	var root, pageSize int64
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT rootpage FROM sqlite_master WHERE name = ?`, "sqlite_autoindex_nodes_1").Scan(&root))
	require.NoError(t, db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize))
	rowids := map[string]int64{}
	for from, to := range repoint {
		for _, id := range []string{from, to} {
			var rowid int64
			require.NoError(t, db.QueryRowContext(ctx, `SELECT rowid FROM nodes WHERE id = ?`, id).Scan(&rowid))
			require.Less(t, rowid, int64(128), "rowid %d of %s must fit in one byte", rowid, id)
			rowids[id] = rowid
		}
	}
	require.NoError(t, db.Close())

	raw, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	page := raw[(root-1)*pageSize : root*pageSize]
	require.Equal(t, byte(0x0a), page[0], "the index root is a leaf page")
	patched := 0
	for i := 0; i < int(binary.BigEndian.Uint16(page[3:5])); i++ {
		cell := int(binary.BigEndian.Uint16(page[8+2*i:]))
		_, n := sqliteVarint(page[cell:]) // payload size
		rec := cell + n
		headerLen, n := sqliteVarint(page[rec:])
		idType, n2 := sqliteVarint(page[rec+n:])
		rowidType, _ := sqliteVarint(page[rec+n+n2:])
		idStart := rec + int(headerLen)
		id := string(page[idStart : idStart+int((idType-13)/2)])
		to, ok := repoint[id]
		if !ok {
			continue
		}
		require.Equal(t, uint64(1), rowidType, "the entry's rowid is stored in one byte")
		require.Equal(t, byte(rowids[id]), page[idStart+len(id)])
		page[idStart+len(id)] = byte(rowids[to])
		patched++
	}
	require.Equal(t, len(repoint), patched, "every entry to repoint was found")
	require.NoError(t, os.WriteFile(dbPath, raw, 0o600))
}

// damagedIndexFixture seeds RC-1, RC-2 and RC-3 (and RC-4 when rc4 is set);
// RC-1 has a comment, a torn
// annotations cell and no uid in the database (so the uid check cannot tell
// tasks apart), and the mirror gives RC-2 a comment of its own. The mirror
// is exported with editMirror applied, then the index entries in repoint
// are patched. It returns the database and mirror paths (MTIX-95.31.3).
func damagedIndexFixture(
	t *testing.T, editMirror func(t *testing.T, m *ExportData), repoint map[string]string, rc4 bool,
) (dbPath, mirrorPath string) {
	t.Helper()
	s, dbPath := seedMirrorColumnFixture(t)
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	extra := map[int]string{3: "third task"}
	if rc4 {
		extra[4] = "fourth task"
	}
	for seq := 3; seq <= 4; seq++ {
		title, ok := extra[seq]
		if !ok {
			continue
		}
		require.NoError(t, s.CreateNode(context.Background(), &model.Node{
			ID: fmt.Sprintf("RC-%d", seq), Project: "RC", Depth: 0, Seq: seq, Title: title,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h", CreatedAt: at, UpdatedAt: at,
		}))
	}
	mirrorPath, _ = exportMirrorOf(t, s, func(t *testing.T, m *ExportData) {
		mirrorNodeOf(t, m, "RC-2").Annotations = []model.Annotation{{
			ID: rc2CommentID, Author: "teammate", Text: rc2CommentText, CreatedAt: at.Add(time.Hour),
		}}
		if editMirror != nil {
			editMirror(t, m)
		}
	}, false)
	_, err := s.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET uid = NULL WHERE id = ?`, "RC-1")
	require.NoError(t, err)
	corruptAndClose(t, s, "annotations")
	repointIndexEntries(t, dbPath, repoint)
	return dbPath, mirrorPath
}

// notesContaining returns the notes that contain text.
func notesContaining(notes []string, text string) []string {
	var out []string
	for _, n := range notes {
		if strings.Contains(n, text) {
			out = append(out, n)
		}
	}
	return out
}

// TestRecover_IndexEntryPointsAtAnotherRow_ExportHoldsEachIDOnceAndImports
// covers a damaged primary-key index (MTIX-95.31.3): an entry for key A
// points at the row of B. The row keeps its own id and is exported once;
// nothing is kept under key A, which is not listed as recovered; a note for
// A says the row read under A carries B; A then comes from the mirror, or
// is listed as lost; and the salvage file imports into a fresh store with
// a replace import. A mirror copy never reaches a row through another
// task's entry: RC-2's comment is only ever on RC-2, and RC-1's comment is
// restored once, on RC-1.
func TestRecover_IndexEntryPointsAtAnotherRow_ExportHoldsEachIDOnceAndImports(t *testing.T) {
	dropRC2 := func(t *testing.T, m *ExportData) {
		kept := m.Nodes[:0]
		for _, n := range m.Nodes {
			if n.ID != "RC-2" {
				kept = append(kept, n)
			}
		}
		m.Nodes = kept
	}
	tests := []struct {
		name          string
		editMirror    func(t *testing.T, m *ExportData)
		repoint       map[string]string
		rc4           bool
		wantNotes     []string // one note each, for the entry that misread
		wantRecovered []string
		wantMirror    []string
		wantLost      []string
		wantTitles    map[string]string
	}{
		{
			name:    "entry of RC-2 points at RC-1's row, which RC-1's own entry reaches; mirror holds RC-2",
			repoint: map[string]string{"RC-2": "RC-1"},
			wantNotes: []string{"node RC-2: the database row read under id RC-2 carries id RC-1, " +
				"so the row of RC-2 could not be read through the primary-key index; " +
				"the entry of RC-1 reaches that row, so it is not salvaged again"},
			wantRecovered: []string{"RC-1", "RC-3"},
			wantMirror:    []string{"RC-2"},
			wantTitles: map[string]string{
				"RC-1": "reviewed task with a comment", "RC-2": "unaffected neighbour task", "RC-3": "third task",
			},
		},
		{
			name:       "entry of RC-2 points at RC-1's row; mirror lacks RC-2",
			editMirror: dropRC2,
			repoint:    map[string]string{"RC-2": "RC-1"},
			wantNotes: []string{"node RC-2: the database row read under id RC-2 carries id RC-1",
				"row(s) were unreadable in the database and absent from the mirror"},
			wantRecovered: []string{"RC-1", "RC-3"},
			wantLost:      []string{"RC-2"},
			wantTitles:    map[string]string{"RC-1": "reviewed task with a comment", "RC-3": "third task"},
		},
		{
			name:    "entry of RC-2 points at RC-3's row, which RC-3's own later entry reaches",
			repoint: map[string]string{"RC-2": "RC-3"},
			wantNotes: []string{"node RC-2: the database row read under id RC-2 carries id RC-3, " +
				"so the row of RC-2 could not be read through the primary-key index; " +
				"the entry of RC-3 reaches that row, so it is not salvaged again"},
			wantRecovered: []string{"RC-1", "RC-3"},
			wantMirror:    []string{"RC-2"},
			wantTitles: map[string]string{
				"RC-1": "reviewed task with a comment", "RC-2": "unaffected neighbour task", "RC-3": "third task",
			},
		},
		{
			name:    "entries of RC-2 and RC-3 point at each other's rows",
			repoint: map[string]string{"RC-2": "RC-3", "RC-3": "RC-2"},
			wantNotes: []string{
				"node RC-2: the database row read under id RC-2 carries id RC-3, " +
					"so the row of RC-2 could not be read through the primary-key index; " +
					"no entry of RC-3 reaches that row, so it is salvaged once, as RC-3",
				"node RC-3: the database row read under id RC-3 carries id RC-2, " +
					"so the row of RC-3 could not be read through the primary-key index; " +
					"no entry of RC-2 reaches that row, so it is salvaged once, as RC-2",
			},
			wantRecovered: []string{"RC-1", "RC-2", "RC-3"},
			wantTitles: map[string]string{
				"RC-1": "reviewed task with a comment", "RC-2": "unaffected neighbour task", "RC-3": "third task",
			},
		},
		{
			name:    "entries of RC-2 and RC-3 point at RC-4's row, RC-4's entry at RC-1's row",
			rc4:     true,
			repoint: map[string]string{"RC-2": "RC-4", "RC-3": "RC-4", "RC-4": "RC-1"},
			wantNotes: []string{
				"node RC-2: the database row read under id RC-2 carries id RC-4, " +
					"so the row of RC-2 could not be read through the primary-key index; " +
					"no entry of RC-4 reaches that row, so it is salvaged once, as RC-4",
				"node RC-3: the database row read under id RC-3 carries id RC-4, " +
					"so the row of RC-3 could not be read through the primary-key index; " +
					"a row is already salvaged as RC-4, so this one is not salvaged again",
				"node RC-4: the database row read under id RC-4 carries id RC-1, " +
					"so the row of RC-4 could not be read through the primary-key index; " +
					"the entry of RC-1 reaches that row, so it is not salvaged again",
			},
			wantRecovered: []string{"RC-1", "RC-4"},
			wantMirror:    []string{"RC-2", "RC-3"},
			wantTitles: map[string]string{
				"RC-1": "reviewed task with a comment", "RC-2": "unaffected neighbour task",
				"RC-3": "third task", "RC-4": "fourth task",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath, mirrorPath := damagedIndexFixture(t, tt.editMirror, tt.repoint, tt.rc4)

			res, err := Recover(context.Background(), dbPath, mirrorPath, "test-version", slog.Default())
			require.NoError(t, err)

			for _, want := range tt.wantNotes {
				assert.Len(t, notesContaining(res.Notes, want), 1, "note %q; notes: %v", want, res.Notes)
			}
			for _, n := range notesContaining(res.Notes, "carries id") {
				assert.NotContains(t, n, "restored")
				assert.NotContains(t, n, "recovered")
			}
			assert.Equal(t, tt.wantRecovered, res.RecoveredIDs)
			assert.Equal(t, tt.wantMirror, res.FromMirror)
			assert.Equal(t, tt.wantLost, res.LostIDs)

			titles := map[string]string{}
			for _, n := range res.Export.Nodes {
				_, dup := titles[n.ID]
				assert.False(t, dup, "node %s is exported once", n.ID)
				titles[n.ID] = n.Title
				for _, a := range n.Annotations {
					if a.ID == rc2CommentID {
						assert.Equal(t, "RC-2", n.ID, "RC-2's comment is only ever on RC-2")
					}
				}
			}
			for id, title := range tt.wantTitles {
				assert.Equal(t, title, titles[id], "node %s", id)
			}
			assert.Len(t, titles, len(tt.wantTitles))
			rc1 := recoveredNode(t, res, "RC-1")
			require.Len(t, rc1.Annotations, 1, "RC-1's comment is restored once, on RC-1")
			assert.Equal(t, rcCommentID, rc1.Annotations[0].ID)

			fresh, err := New(filepath.Join(t.TempDir(), "fresh.db"), slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { _ = fresh.Close() })
			_, err = fresh.Import(context.Background(), res.Export, ImportModeReplace, false)
			require.NoError(t, err, "the salvage file imports with a replace import")
		})
	}
}
