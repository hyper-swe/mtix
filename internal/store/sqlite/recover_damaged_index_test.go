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
	"encoding/json"
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

// indexPatch rewrites one primary-key index entry (MTIX-95.31.3): renameTo
// replaces the id it indexes (same length), pointAt makes it point at the
// row of that node; "" leaves either as it is.
type indexPatch struct{ renameTo, pointAt string }

// patchIndexEntries applies patches, keyed by the id each entry indexes, to
// the primary-key index of the closed database at dbPath (MTIX-95.31.3): the
// damage a torn index page can leave. It folds the write-ahead log into the
// file and looks every rowid up before patching anything, then rewrites the
// entries in the index's root page, which must be a leaf page (a small
// store); every rowid involved must fit in one byte, and an entry that is
// repointed must store its rowid in one byte (a rowid above 1).
func patchIndexEntries(t *testing.T, dbPath string, patches map[string]indexPatch) {
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
	for from, p := range patches {
		for _, id := range []string{from, p.pointAt} {
			if id == "" {
				continue
			}
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
		p, ok := patches[id]
		if !ok {
			continue
		}
		if p.pointAt != "" {
			require.Equal(t, uint64(1), rowidType, "the entry's rowid is stored in one byte")
			require.Equal(t, byte(rowids[id]), page[idStart+len(id)])
			page[idStart+len(id)] = byte(rowids[p.pointAt])
		}
		if p.renameTo != "" {
			require.Len(t, p.renameTo, len(id), "a renamed entry keeps its length")
			copy(page[idStart:], p.renameTo)
		}
		patched++
	}
	require.Equal(t, len(patches), patched, "every entry to patch was found")
	require.NoError(t, os.WriteFile(dbPath, raw, 0o600))
}

// renameTableRows rewrites the id that each row of the nodes table carries,
// keyed by the id it carries now, in the closed database at dbPath
// (MTIX-107.78): the damage that leaves the table holding two rows with one
// id. The index is left as it is. It folds the write-ahead log into the file,
// then rewrites the rows in the table's root page, which must be a leaf page
// (a small store) whose cells hold their whole payload; id is the table's
// first column, and a renamed row keeps its id's length.
func renameTableRows(t *testing.T, dbPath string, renames map[string]string) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `PRAGMA journal_mode = DELETE`)
	require.NoError(t, err)
	var root, pageSize int64
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT rootpage FROM sqlite_master WHERE type = 'table' AND name = ?`, "nodes").Scan(&root))
	require.NoError(t, db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize))
	require.NoError(t, db.Close())

	raw, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	page := raw[(root-1)*pageSize : root*pageSize]
	require.Equal(t, byte(0x0d), page[0], "the table root is a leaf page")
	renamed := 0
	for i := 0; i < int(binary.BigEndian.Uint16(page[3:5])); i++ {
		cell := int(binary.BigEndian.Uint16(page[8+2*i:]))
		payload, n := sqliteVarint(page[cell:])
		require.Less(t, payload, uint64(pageSize-35), "the row's payload fits in its cell")
		_, n2 := sqliteVarint(page[cell+n:]) // rowid
		rec := cell + n + n2
		headerLen, n := sqliteVarint(page[rec:])
		idType, _ := sqliteVarint(page[rec+n:])
		idStart := rec + int(headerLen)
		id := string(page[idStart : idStart+int((idType-13)/2)])
		to, ok := renames[id]
		if !ok {
			continue
		}
		require.Len(t, to, len(id), "a renamed row keeps its id's length")
		copy(page[idStart:], to)
		renamed++
	}
	require.Equal(t, len(renames), renamed, "every row to rename was found")
	require.NoError(t, os.WriteFile(dbPath, raw, 0o600))
}

// repointIndexEntries makes the primary-key index entry of each node id in
// repoint point at the row of the node it maps to (patchIndexEntries).
func repointIndexEntries(t *testing.T, dbPath string, repoint map[string]string) {
	t.Helper()
	patches := map[string]indexPatch{}
	for from, to := range repoint {
		patches[from] = indexPatch{pointAt: to}
	}
	patchIndexEntries(t, dbPath, patches)
}

// damagedIndexCase is one damaged-index scenario and what recover must
// report for it (MTIX-95.31.3).
type damagedIndexCase struct {
	name          string
	editMirror    func(t *testing.T, m *ExportData)
	seedContent   func(t *testing.T, s *Store)
	patches       map[string]indexPatch
	renameRows    map[string]string // ids of table rows to rewrite (renameTableRows)
	rc4           bool              // also seed RC-4
	tearRC2       bool              // also tear RC-2's annotations cell
	tearRC3       bool              // also tear RC-3's annotations cell
	wantNotes     []string          // each exactly once
	wantRecovered []string
	wantMirror    []string
	wantLost      []string
	wantTitles    map[string]string
}

// damagedIndexFixture seeds RC-1, RC-2 and RC-3 (and RC-4 when tc.rc4 is
// set). RC-1 has a comment, a torn annotations cell and no uid in the
// database (so the uid check cannot tell tasks apart); RC-2's annotations
// cell is torn too when tc.tearRC2 is set; and the mirror gives RC-2 a
// comment of its own. The mirror is exported with tc.editMirror applied,
// then the index entries in tc.patches are patched. It returns the database
// and mirror paths (MTIX-95.31.3).
func damagedIndexFixture(t *testing.T, tc *damagedIndexCase) (dbPath, mirrorPath string) {
	t.Helper()
	s, dbPath := seedMirrorColumnFixture(t)
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	extra := map[int]string{3: "third task"}
	if tc.rc4 {
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
	if tc.seedContent != nil {
		tc.seedContent(t, s)
	}
	mirrorPath, _ = exportMirrorOf(t, s, func(t *testing.T, m *ExportData) {
		mirrorNodeOf(t, m, "RC-2").Annotations = []model.Annotation{{
			ID: rc2CommentID, Author: "teammate", Text: rc2CommentText, CreatedAt: at.Add(time.Hour),
		}}
		if tc.editMirror != nil {
			tc.editMirror(t, m)
		}
	}, false)
	_, err := s.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET uid = NULL WHERE id = ?`, "RC-1")
	require.NoError(t, err)
	if tc.tearRC2 {
		_, err = s.WriteDB().ExecContext(context.Background(), corruptColumnSQL(t, "annotations"), tornJSON, "RC-2")
		require.NoError(t, err)
	}
	if tc.tearRC3 {
		_, err = s.WriteDB().ExecContext(context.Background(), corruptColumnSQL(t, "annotations"), tornJSON, "RC-3")
		require.NoError(t, err)
	}
	corruptAndClose(t, s, "annotations")
	patchIndexEntries(t, dbPath, tc.patches)
	if tc.renameRows != nil {
		renameTableRows(t, dbPath, tc.renameRows)
	}
	return dbPath, mirrorPath
}

// indexEntryNoteText is in every note recover writes about a damaged
// primary-key index entry (MTIX-107.80).
const indexEntryNoteText = "index entr"

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

// duplicateMirrorChoiceCase uses positive selectors for candidate rows (1 is
// RC-2, 2 is RC-3); zero is empty, and -1 is an unmatched mirror value.
type duplicateMirrorChoiceCase struct {
	name                                                      string
	rows, mirrorUID, mirrorHash, winner                       int
	reason                                                    string
	ownBroken, misread, earlyMisread                          bool
	sameUID, sameHash, emptyFirstUID, emptyLastHash           bool
	noMirror, missingCopy, invalidTime, ambiguous, unverified bool
	schema                                                    string
	torn                                                      int
	repair                                                    bool
}

type duplicateMirrorChoiceFixture struct {
	dbPath, mirrorPath string
	rows               []salvagedRow
	mirror             exportNode
}

func duplicateMirrorUIDCases() []duplicateMirrorChoiceCase {
	return []duplicateMirrorChoiceCase{
		{name: "damaged own entry real row read second", ownBroken: true, mirrorUID: 1, mirrorHash: 2, winner: 1, reason: "uid-match"},
		{name: "UID beats conflicting hash for second row", mirrorUID: 2, mirrorHash: 1, winner: 2, reason: "uid-match"},
		{name: "matching row reached through misread entry", misread: true, mirrorUID: 2, mirrorHash: 1, winner: 2, reason: "uid-match"},
		{name: "hash fallback despite differing UIDs", mirrorUID: -1, mirrorHash: 2, winner: 2, reason: "hash-match"},
		{name: "hash breaks nonempty UID tie", sameUID: true, mirrorUID: 1, mirrorHash: 2, winner: 2, reason: "uid-match then hash-match"},
		{name: "three rows only final UID and hash match", rows: 3, mirrorUID: 3, mirrorHash: 3, winner: 3, reason: "uid-match"},
		{name: "three rows prefer final UID match", rows: 3, mirrorUID: 3, mirrorHash: 1, winner: 3, reason: "uid-match"},
		{name: "three misread candidates improve twice", rows: 3, misread: true, mirrorUID: 3, mirrorHash: 2, winner: 3, reason: "uid-match"},
		{name: "three UID matches use final hash", rows: 3, sameUID: true, mirrorUID: 1, mirrorHash: 3, winner: 3, reason: "uid-match then hash-match"},
	}
}

func duplicateMirrorFallbackCases() []duplicateMirrorChoiceCase {
	return []duplicateMirrorChoiceCase{
		{name: "no mirror keeps first", noMirror: true, winner: 1, reason: "first-kept"},
		{name: "neither value matches", mirrorUID: -1, mirrorHash: -1, winner: 1, reason: "first-kept"},
		{name: "equal UID and hash keep first", sameUID: true, sameHash: true, mirrorUID: 1, mirrorHash: 1, winner: 1, reason: "uid-match then first-kept among equal matches"},
		{name: "equal hashes keep first", sameHash: true, mirrorHash: 1, winner: 1, reason: "hash-match then first-kept among equal matches"},
		{name: "invalid mirror time cannot choose", mirrorUID: 2, mirrorHash: 2, invalidTime: true, winner: 1, reason: "first-kept"},
		{name: "ambiguous same ID mirror cannot choose", mirrorUID: 2, mirrorHash: 2, ambiguous: true, winner: 1, reason: "first-kept"},
		{name: "missing mirror node keeps first", missingCopy: true, winner: 1, reason: "first-kept"},
		{name: "empty values are not matches", winner: 1, reason: "first-kept"},
		{name: "empty UID cannot outrank real hash", emptyFirstUID: true, mirrorHash: 2, winner: 2, reason: "hash-match"},
		{name: "empty hash cannot select last", emptyLastHash: true, mirrorUID: -1, winner: 1, reason: "first-kept"},
		{name: "legacy mirror has no extra schema floor", schema: "1.0.0", mirrorUID: 2, mirrorHash: 1, winner: 2, reason: "uid-match"},
		{name: "unverified mirror remains salvage source", unverified: true, mirrorUID: 2, mirrorHash: 1, winner: 2, reason: "uid-match (the mirror checksum did not verify)"},
		{name: "unverified hash match marks the choice", unverified: true, mirrorUID: -1, mirrorHash: 2, winner: 2, reason: "hash-match (the mirror checksum did not verify)"},
		{name: "unverified no match keeps first without choice marker", unverified: true, mirrorUID: -1, mirrorHash: -1, winner: 1, reason: "first-kept"},
		{name: "own entry fallback precedes earlier misread", earlyMisread: true, missingCopy: true, winner: 1, reason: "first-kept"},
	}
}

func TestRecover_DuplicateRowsChooseMirror(t *testing.T) {
	cases := append(duplicateMirrorUIDCases(), duplicateMirrorFallbackCases()...)
	cases = append(cases,
		duplicateMirrorChoiceCase{name: "losing torn column never repairs winner", ownBroken: true, mirrorUID: 1, mirrorHash: 2, torn: 2, winner: 1, reason: "uid-match"},
		duplicateMirrorChoiceCase{name: "winning torn column repairs from mirror", misread: true, mirrorUID: 2, mirrorHash: 1, torn: 2, repair: true, winner: 2, reason: "uid-match"})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := makeDuplicateMirrorChoiceFixture(t, tc)
			res, err := Recover(context.Background(), f.dbPath, f.mirrorPath, "test-version", slog.Default())
			require.NoError(t, err)
			want := f.rows[tc.winner-1].node
			got := recoveredNode(t, res, "RC-2")
			require.Equal(t, want.Title, got.Title, "winner title must identify the selected physical row")
			require.Equal(t, want.UID, got.UID, "winner UID")
			annotations := want.Annotations
			if tc.repair {
				annotations = f.mirror.Annotations
			}
			assert.Equal(t, annotations, got.Annotations, "only the winning row's unreadable columns may be repaired")
			recovered := []string{"RC-1", "RC-2"}
			if tc.earlyMisread {
				recovered = []string{"RC-2"}
			}
			assert.Equal(t, recovered, res.RecoveredIDs)
			assertDuplicateMirrorChoiceNotes(t, tc, f.rows, res)
			fresh, err := New(filepath.Join(t.TempDir(), "fresh.db"), slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { _ = fresh.Close() })
			_, err = fresh.Import(context.Background(), res.Export, ImportModeReplace, false)
			require.NoError(t, err, "the selected export imports with unique IDs and UIDs")
		})
	}
}

func makeDuplicateMirrorChoiceFixture(t *testing.T, tc duplicateMirrorChoiceCase) duplicateMirrorChoiceFixture {
	t.Helper()
	count := tc.rows
	if count == 0 {
		count = 2
	}
	s, dbPath := seedMirrorColumnFixture(t)
	seedDuplicateMirrorCandidates(t, s, count, tc)
	var mirror exportNode
	mirrorPath, _ := exportMirrorOf(t, s, func(t *testing.T, m *ExportData) {
		mirror = editDuplicateChoiceMirror(t, m, tc)
	}, tc.unverified)
	changeDuplicateChoiceUIDs(t, s, count, tc)
	if tc.earlyMisread {
		// The byte-patch helper needs an entry with a one-byte rowid payload.
		_, err := s.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET rowid = ? WHERE id = ?`, 20, "RC-1")
		require.NoError(t, err)
	}
	if tc.torn > 0 {
		_, err := s.WriteDB().ExecContext(context.Background(), corruptColumnSQL(t, "annotations"), tornJSON, fmt.Sprintf("RC-%d", tc.torn+1))
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())
	patches := map[string]indexPatch{}
	renames := map[string]string{}
	for seq := 3; seq <= count+1; seq++ {
		id := fmt.Sprintf("RC-%d", seq)
		renames[id] = "RC-2"
		if !tc.misread && !tc.earlyMisread {
			patches[id] = indexPatch{renameTo: "RC-2"}
		}
	}
	if tc.ownBroken {
		patches["RC-2"] = indexPatch{pointAt: "RC-3"}
		patches["RC-3"] = indexPatch{renameTo: "RC-2", pointAt: "RC-2"}
	}
	if tc.earlyMisread {
		patches["RC-1"] = indexPatch{pointAt: "RC-3"}
	}
	patchIndexEntries(t, dbPath, patches)
	renameTableRows(t, dbPath, renames)
	if tc.noMirror {
		mirrorPath = ""
	}
	return duplicateMirrorChoiceFixture{dbPath: dbPath, mirrorPath: mirrorPath, rows: readDuplicateChoiceRows(t, dbPath, count), mirror: mirror}
}

func seedDuplicateMirrorCandidates(t *testing.T, s *Store, count int, tc duplicateMirrorChoiceCase) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for seq := 3; seq <= count+1; seq++ {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: fmt.Sprintf("RC-%d", seq), Project: "RC", Seq: seq, Title: fmt.Sprintf("duplicate candidate %d", seq-1),
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeIssue, CreatedAt: at, UpdatedAt: at,
		}))
	}
	for seq := 2; seq <= count+1; seq++ {
		hash := fmt.Sprintf("row-%d-hash", seq)
		if tc.sameHash {
			hash = "row-2-hash"
		}
		if tc.emptyLastHash && seq == count+1 {
			hash = ""
		}
		result, err := s.WriteDB().ExecContext(ctx, `UPDATE nodes SET content_hash = ? WHERE id = ?`, hash, fmt.Sprintf("RC-%d", seq))
		require.NoError(t, err)
		n, err := result.RowsAffected()
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
	}
}

func editDuplicateChoiceMirror(t *testing.T, m *ExportData, tc duplicateMirrorChoiceCase) exportNode {
	t.Helper()
	n := mirrorNodeOf(t, m, "RC-2")
	n.UID = duplicateMirrorValue(t, m, tc.mirrorUID, true)
	n.ContentHash = duplicateMirrorValue(t, m, tc.mirrorHash, false)
	n.Annotations = []model.Annotation{{ID: rc2CommentID, Author: "fixture", Text: "mirror-only column", CreatedAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)}}
	if tc.invalidTime {
		n.CreatedAt = "not-a-time"
	}
	if tc.schema != "" {
		m.SchemaVersion = tc.schema
	}
	copyOfNode := *n
	kept := []exportNode{*mirrorNodeOf(t, m, "RC-1")}
	if !tc.missingCopy {
		kept = append(kept, copyOfNode)
	}
	if tc.ambiguous {
		kept = append(kept, copyOfNode)
	}
	m.Nodes = kept
	return copyOfNode
}

func duplicateMirrorValue(t *testing.T, m *ExportData, which int, uid bool) string {
	t.Helper()
	if which < 0 {
		return "unmatched-fixture-value"
	}
	if which == 0 {
		return ""
	}
	n := mirrorNodeOf(t, m, fmt.Sprintf("RC-%d", which+1))
	if uid {
		return n.UID
	}
	return n.ContentHash
}

func changeDuplicateChoiceUIDs(t *testing.T, s *Store, count int, tc duplicateMirrorChoiceCase) {
	t.Helper()
	ctx := context.Background()
	if tc.sameUID {
		_, err := s.WriteDB().ExecContext(ctx, `DROP INDEX idx_nodes_uid`)
		require.NoError(t, err)
		var uid string
		require.NoError(t, s.WriteDB().QueryRowContext(ctx, `SELECT uid FROM nodes WHERE id = ?`, "RC-2").Scan(&uid))
		require.NotEmpty(t, uid)
		for seq := 3; seq <= count+1; seq++ {
			_, err = s.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = ? WHERE id = ?`, uid, fmt.Sprintf("RC-%d", seq))
			require.NoError(t, err)
		}
	}
	if tc.emptyFirstUID {
		_, err := s.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = ? WHERE id = ?`, "", "RC-2")
		require.NoError(t, err)
	}
}

func readDuplicateChoiceRows(t *testing.T, path string, count int) []salvagedRow {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?mode=ro")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	var rows []salvagedRow
	for i := 0; i < count; i++ {
		row, err := readSalvageRow(context.Background(), db, salvageKey{rowid: int64(i + 2)})
		require.NoError(t, err)
		require.Equal(t, "RC-2", row.node.ID)
		rows = append(rows, row)
	}
	require.NoError(t, db.Close())
	return rows
}

func assertDuplicateMirrorChoiceNotes(t *testing.T, tc duplicateMirrorChoiceCase, rows []salvagedRow, res *RecoverResult) {
	t.Helper()
	if tc.misread || tc.earlyMisread {
		assertDuplicateMirrorIndexNotes(t, tc, len(rows), res)
	}
	assert.Len(t, notesContaining(res.Notes, "is not salvaged; its content is "), len(rows)-1)
	for i, row := range rows {
		if i == tc.winner-1 {
			continue
		}
		marker := fmt.Sprintf("the row at rowid %d is not salvaged; its content is ", row.key.rowid)
		notes := notesContaining(res.Notes, marker)
		require.Len(t, notes, 1, "each physical losing row is reported once: %v", res.Notes)
		note := notes[0]
		assert.Contains(t, note, fmt.Sprintf("the row at rowid %d is salvaged as RC-2", rows[tc.winner-1].key.rowid), "every note names the final winner")
		reason, _, ok := strings.Cut(note, "; node ")
		require.True(t, ok, "the report starts with its reason clause")
		assert.Equal(t, "kept the row by "+tc.reason, reason, "the report explains the exact selection tier")
		_, payload, ok := strings.Cut(note, "its content is ")
		require.True(t, ok)
		decoder := json.NewDecoder(strings.NewReader(payload))
		var reported exportNode
		require.NoError(t, decoder.Decode(&reported))
		expected, err := json.Marshal(row.node)
		require.NoError(t, err)
		assert.JSONEq(t, string(expected), payload[:decoder.InputOffset()], "all readable losing-row fields survive")
		tail := strings.TrimSpace(payload[decoder.InputOffset():])
		if len(row.cols) == 0 {
			assert.Empty(t, tail)
		}
		for _, col := range row.cols {
			assert.Contains(t, tail, "its "+col.column+" column could not be read")
		}
	}
}

func assertDuplicateMirrorIndexNotes(t *testing.T, tc duplicateMirrorChoiceCase, count int, res *RecoverResult) {
	t.Helper()
	for candidate := 2; candidate <= count; candidate++ {
		id := fmt.Sprintf("RC-%d", candidate+1)
		suffix := "the table holds two rows with id RC-2, so it is not salvaged (see the note on RC-2)"
		if candidate == tc.winner {
			suffix = "no entry of RC-2 reaches that row, so it is salvaged once, as RC-2"
		}
		prefix := fmt.Sprintf("node %s: the index entry for %s points at the row of RC-2; ", id, id)
		assert.Len(t, notesContaining(res.Notes, prefix+suffix), 1, "index notes must describe the final winner")
	}
	if tc.earlyMisread {
		assert.Len(t, notesContaining(res.Notes, "node RC-1: the index entry for RC-1 points at the row of RC-2; the table holds two rows"), 1)
	}
}

// A repeated entry for the original winner must not claim that row was kept
// after a later mirror match displaces it (MTIX-107.78.2).
func TestRecover_DuplicateOwnEntryReportsFinalWinner(t *testing.T) {
	s, path := seedMirrorColumnFixture(t)
	tc := duplicateMirrorChoiceCase{mirrorUID: 1, mirrorHash: 2, winner: 1, reason: "uid-match"}
	seedDuplicateMirrorCandidates(t, s, 3, tc)
	mirrorPath, _ := exportMirrorOf(t, s, func(t *testing.T, m *ExportData) {
		extra := *mirrorNodeOf(t, m, "RC-4")
		editDuplicateChoiceMirror(t, m, tc)
		m.Nodes = append(m.Nodes, extra)
	}, false)
	require.NoError(t, s.Close())
	patchIndexEntries(t, path, map[string]indexPatch{
		"RC-2": {pointAt: "RC-3"},
		"RC-3": {renameTo: "RC-2", pointAt: "RC-3"},
		"RC-4": {renameTo: "RC-2", pointAt: "RC-2"},
	})
	renameTableRows(t, path, map[string]string{"RC-3": "RC-2"})
	rows := readDuplicateChoiceRows(t, path, 2)
	res, err := Recover(context.Background(), path, mirrorPath, "test-version", slog.Default())
	require.NoError(t, err)
	require.Equal(t, rows[0].node.UID, recoveredNode(t, res, "RC-2").UID)
	assertDuplicateMirrorChoiceNotes(t, tc, rows, res)
	assert.Len(t, notesContaining(res.Notes, "same discarded duplicate row"), 1)
	assert.Empty(t, notesContaining(res.Notes, "same row, which is salvaged once"))
	fresh, err := New(filepath.Join(t.TempDir(), "fresh.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = fresh.Close() })
	_, err = fresh.Import(context.Background(), res.Export, ImportModeReplace, false)
	require.NoError(t, err)
}

// TestRecover_DuplicateWinningOwnEntryRepeatedOnce pins the winner flag after
// replacement: the repeated cell reaches the NEW winner (MTIX-107.78.2 F3).
func TestRecover_DuplicateWinningOwnEntryRepeatedOnce(t *testing.T) {
	s, path := seedMirrorColumnFixture(t)
	tc := duplicateMirrorChoiceCase{mirrorUID: 1, mirrorHash: 2, winner: 1, reason: "uid-match"}
	seedDuplicateMirrorCandidates(t, s, 3, tc)
	mirrorPath, _ := exportMirrorOf(t, s, func(t *testing.T, m *ExportData) {
		extra := *mirrorNodeOf(t, m, "RC-4")
		editDuplicateChoiceMirror(t, m, tc)
		m.Nodes = append(m.Nodes, extra)
	}, false)
	require.NoError(t, s.Close())
	patchIndexEntries(t, path, map[string]indexPatch{
		"RC-2": {pointAt: "RC-3"},
		"RC-3": {renameTo: "RC-2", pointAt: "RC-2"},
		"RC-4": {renameTo: "RC-2", pointAt: "RC-2"},
	})
	renameTableRows(t, path, map[string]string{"RC-3": "RC-2"})
	rows := readDuplicateChoiceRows(t, path, 2)
	res, err := Recover(context.Background(), path, mirrorPath, "test-version", slog.Default())
	require.NoError(t, err)
	require.Equal(t, rows[0].node.UID, recoveredNode(t, res, "RC-2").UID)
	assertDuplicateMirrorChoiceNotes(t, tc, rows, res)
	assertWinnerNeverDiscarded(t, res, rows[0].key.rowid)
	assert.Len(t, notesContaining(res.Notes, "same row, which is salvaged once"), 1)
	assert.Empty(t, notesContaining(res.Notes, "same discarded duplicate row"))
	assertDuplicateChoiceImports(t, res)
}

// TestRecover_DuplicateMisreadsReachKeptRow pins same-row reuse when B has no
// working own entry and two other entries reach B (MTIX-107.78.2 F2).
func TestRecover_DuplicateMisreadsReachKeptRow(t *testing.T) {
	s, path := seedMirrorColumnFixture(t)
	tc := duplicateMirrorChoiceCase{mirrorUID: 1, mirrorHash: 1}
	seedDuplicateMirrorCandidates(t, s, 3, tc)
	mirrorPath, _ := exportMirrorOf(t, s, func(t *testing.T, m *ExportData) {
		editDuplicateChoiceMirror(t, m, tc)
	}, false)
	require.NoError(t, s.Close())
	patchIndexEntries(t, path, map[string]indexPatch{
		"RC-2": {pointAt: "RC-1"},
		"RC-3": {pointAt: "RC-2"},
		"RC-4": {pointAt: "RC-2"},
	})
	rows := readDuplicateChoiceRows(t, path, 1)
	res, err := Recover(context.Background(), path, mirrorPath, "test-version", slog.Default())
	require.NoError(t, err)
	require.Equal(t, rows[0].node.UID, recoveredNode(t, res, "RC-2").UID)
	assertWinnerNeverDiscarded(t, res, rows[0].key.rowid)
	assert.Empty(t, notesContaining(res.Notes, "is not salvaged; its content is "))
	prefix := "the index entry for %s points at the row of RC-2; "
	first := "node RC-3: " + fmt.Sprintf(prefix, "RC-3") + "no entry of RC-2 reaches that row, so it is salvaged once, as RC-2"
	again := "node RC-4: " + fmt.Sprintf(prefix, "RC-4") + "a row is already salvaged as RC-2, so this one is not salvaged again"
	assert.Equal(t, []string{first}, notesContaining(res.Notes, "node RC-3: "))
	assert.Equal(t, []string{again}, notesContaining(res.Notes, "node RC-4: "))
	assertDuplicateChoiceImports(t, res)
}

// TestRecover_DuplicateOwnLoserIndexNotes distinguishes a displaced own row
// and a later losing own row from a salvaged row (MTIX-107.78.2 F4).
func TestRecover_DuplicateOwnLoserIndexNotes(t *testing.T) {
	for _, winner := range []int{1, 2} {
		t.Run(fmt.Sprintf("winner_%d", winner), func(t *testing.T) {
			tc := duplicateMirrorChoiceCase{mirrorUID: winner, mirrorHash: 3 - winner, winner: winner, reason: "uid-match"}
			f := makeDuplicateOwnLoserFixture(t, tc)
			res, err := Recover(context.Background(), f.dbPath, f.mirrorPath, "test-version", slog.Default())
			require.NoError(t, err)
			require.Equal(t, f.rows[winner-1].node.UID, recoveredNode(t, res, "RC-2").UID)
			assertDuplicateMirrorChoiceNotes(t, tc, f.rows, res)
			assertWinnerNeverDiscarded(t, res, f.rows[winner-1].key.rowid)
			note := "node RC-4: the index entry for RC-4 points at the row of RC-2; " +
				"the table holds two rows with id RC-2, so it is not salvaged (see the note on RC-2)"
			assert.Equal(t, []string{note}, notesContaining(res.Notes, "node RC-4: "), "the index note must describe the losing own row")
			assertDuplicateChoiceImports(t, res)
		})
	}
}

// makeDuplicateOwnLoserFixture routes RC-4 to the losing row after two own
// entries have competed for RC-2 (MTIX-107.78.2 F4).
func makeDuplicateOwnLoserFixture(t *testing.T, tc duplicateMirrorChoiceCase) duplicateMirrorChoiceFixture {
	t.Helper()
	s, path := seedMirrorColumnFixture(t)
	seedDuplicateMirrorCandidates(t, s, 3, tc)
	mirrorPath, _ := exportMirrorOf(t, s, func(t *testing.T, m *ExportData) {
		editDuplicateChoiceMirror(t, m, tc)
	}, false)
	require.NoError(t, s.Close())
	loser := 3 - tc.winner
	patchIndexEntries(t, path, map[string]indexPatch{
		"RC-3": {renameTo: "RC-2"},
		"RC-4": {pointAt: fmt.Sprintf("RC-%d", loser+1)},
	})
	renameTableRows(t, path, map[string]string{"RC-3": "RC-2"})
	return duplicateMirrorChoiceFixture{dbPath: path, mirrorPath: mirrorPath, rows: readDuplicateChoiceRows(t, path, 2)}
}

// assertWinnerNeverDiscarded catches a winner incorrectly queued as its own
// duplicate loser (MTIX-107.78.2 F2/F3).
func assertWinnerNeverDiscarded(t *testing.T, res *RecoverResult, rowid int64) {
	t.Helper()
	marker := fmt.Sprintf("the row at rowid %d is not salvaged; its content is ", rowid)
	assert.Empty(t, notesContaining(res.Notes, marker), "the final winning row must never be reported as discarded")
}

// assertDuplicateChoiceImports checks unique recovered IDs and replace import
// after the damaged-index cases (MTIX-107.78.2).
func assertDuplicateChoiceImports(t *testing.T, res *RecoverResult) {
	t.Helper()
	seen := map[string]bool{}
	for _, id := range res.RecoveredIDs {
		require.False(t, seen[id], "each recovered ID is listed once")
		seen[id] = true
	}
	require.True(t, seen["RC-2"])
	fresh, err := New(filepath.Join(t.TempDir(), "fresh.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = fresh.Close() })
	_, err = fresh.Import(context.Background(), res.Export, ImportModeReplace, false)
	require.NoError(t, err, "the selected export imports with unique IDs and UIDs")
}

// TestRecover_IndexEntryPointsAtAnotherRow_ExportHoldsEachIDOnceAndImports
// covers a damaged primary-key index (MTIX-95.31.3). When the entry for A
// points at the row of B, the row keeps its own id and is exported once,
// nothing is kept under A, and one note says the index entry for A points
// at the row of B, never that A's row could not be read. A itself ends up
// exactly one way: from the database when another entry reaches A's row,
// from the mirror, or lost. A second entry with the same id and rowid is
// skipped with one note. Every salvage file imports into a fresh store with
// a replace import, a mirror copy never reaches a row through another
// task's entry, and each torn column is noted once.
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
	point := func(to string) indexPatch { return indexPatch{pointAt: to} }
	all3 := map[string]string{
		"RC-1": "reviewed task with a comment", "RC-2": "unaffected neighbour task", "RC-3": "third task",
	}
	tests := []damagedIndexCase{
		{
			name:    "entry of RC-2 points at RC-1's row, which RC-1's own entry reaches; mirror holds RC-2",
			patches: map[string]indexPatch{"RC-2": point("RC-1")},
			wantNotes: []string{"node RC-2: the index entry for RC-2 points at the row of RC-1; " +
				"the entry of RC-1 reaches that row, so it is not salvaged again"},
			wantRecovered: []string{"RC-1", "RC-3"},
			wantMirror:    []string{"RC-2"},
			wantTitles:    all3,
		},
		{
			name:       "entry of RC-2 points at RC-1's row; mirror lacks RC-2",
			editMirror: dropRC2,
			patches:    map[string]indexPatch{"RC-2": point("RC-1")},
			wantNotes: []string{"node RC-2: the index entry for RC-2 points at the row of RC-1; ",
				"row(s) were unreadable in the database and absent from the mirror"},
			wantRecovered: []string{"RC-1", "RC-3"},
			wantLost:      []string{"RC-2"},
			wantTitles:    map[string]string{"RC-1": "reviewed task with a comment", "RC-3": "third task"},
		},
		{
			name:    "entry of RC-2 points at RC-3's row, which RC-3's own later entry reaches",
			patches: map[string]indexPatch{"RC-2": point("RC-3")},
			wantNotes: []string{"node RC-2: the index entry for RC-2 points at the row of RC-3; " +
				"the entry of RC-3 reaches that row, so it is not salvaged again"},
			wantRecovered: []string{"RC-1", "RC-3"},
			wantMirror:    []string{"RC-2"},
			wantTitles:    all3,
		},
		{
			name:    "entries of RC-2 and RC-3 point at each other's rows (both salvaged from the database)",
			patches: map[string]indexPatch{"RC-2": point("RC-3"), "RC-3": point("RC-2")},
			wantNotes: []string{
				"node RC-2: the index entry for RC-2 points at the row of RC-3; " +
					"no entry of RC-3 reaches that row, so it is salvaged once, as RC-3",
				"node RC-3: the index entry for RC-3 points at the row of RC-2; " +
					"no entry of RC-2 reaches that row, so it is salvaged once, as RC-2",
			},
			wantRecovered: []string{"RC-1", "RC-2", "RC-3"},
			wantTitles:    all3,
		},
		{
			name:    "a second RC-2 entry points at RC-3's row (RC-3's entry renamed RC-2)",
			patches: map[string]indexPatch{"RC-3": {renameTo: "RC-2"}},
			wantNotes: []string{"node RC-2: the index entry for RC-2 points at the row of RC-3; " +
				"no entry of RC-3 reaches that row, so it is salvaged once, as RC-3"},
			wantRecovered: []string{"RC-1", "RC-2", "RC-3"},
			wantTitles:    all3,
		},
		{
			name:    "a second RC-2 entry points at RC-2's own row (RC-3's entry renamed RC-2 and repointed)",
			patches: map[string]indexPatch{"RC-3": {renameTo: "RC-2", pointAt: "RC-2"}},
			tearRC2: true,
			wantNotes: []string{
				"node RC-2: a second index entry for RC-2 points at the same row, which is salvaged once",
			},
			wantRecovered: []string{"RC-1", "RC-2"},
			wantMirror:    []string{"RC-3"},
			wantTitles:    all3,
		},
		{
			name:    "entries of RC-2 and RC-3 point at RC-4's row, RC-4's entry at RC-1's row",
			rc4:     true,
			patches: map[string]indexPatch{"RC-2": point("RC-4"), "RC-3": point("RC-4"), "RC-4": point("RC-1")},
			wantNotes: []string{
				"node RC-2: the index entry for RC-2 points at the row of RC-4; " +
					"no entry of RC-4 reaches that row, so it is salvaged once, as RC-4",
				"node RC-3: the index entry for RC-3 points at the row of RC-4; " +
					"a row is already salvaged as RC-4, so this one is not salvaged again",
				"node RC-4: the index entry for RC-4 points at the row of RC-1; " +
					"the entry of RC-1 reaches that row, so it is not salvaged again",
			},
			wantRecovered: []string{"RC-1", "RC-4"},
			wantMirror:    []string{"RC-2", "RC-3"},
			wantTitles: map[string]string{
				"RC-1": "reviewed task with a comment", "RC-2": "unaffected neighbour task",
				"RC-3": "third task", "RC-4": "fourth task",
			},
		},
		{
			name:       "the table holds two RC-2 rows (RC-3's row renamed), each reached by its own RC-2 entry",
			patches:    map[string]indexPatch{"RC-3": {renameTo: "RC-2"}},
			renameRows: map[string]string{"RC-3": "RC-2"},
			wantNotes: []string{
				"node RC-2: the table holds two rows with id RC-2: the row at rowid 2 is salvaged as RC-2, " +
					"and the row at rowid 3 is not salvaged; its content is ",
				`"title":"third task"`,
			},
			wantRecovered: []string{"RC-1", "RC-2"},
			wantMirror:    []string{"RC-3"},
			wantTitles:    all3,
		},
		{
			name:       "the table holds two RC-2 rows (RC-3's row renamed), the second reached by RC-3's entry",
			patches:    map[string]indexPatch{},
			renameRows: map[string]string{"RC-3": "RC-2"},
			wantNotes: []string{
				"node RC-3: the index entry for RC-3 points at the row of RC-2; " +
					"the table holds two rows with id RC-2, so it is not salvaged (see the note on RC-2)",
				"node RC-2: the table holds two rows with id RC-2: the row at rowid 2 is salvaged as RC-2, " +
					"and the row at rowid 3 is not salvaged; its content is ",
				`"title":"third task"`,
			},
			wantRecovered: []string{"RC-1", "RC-2"},
			wantMirror:    []string{"RC-3"},
			wantTitles:    all3,
		},
		{
			name:       "two entries reach the one extra RC-2 row (RC-3 renamed RC-2, RC-4's entry points at it): reported once",
			rc4:        true,
			patches:    map[string]indexPatch{"RC-4": point("RC-3")},
			renameRows: map[string]string{"RC-3": "RC-2"},
			wantNotes: []string{
				"node RC-3: the index entry for RC-3 points at the row of RC-2; " +
					"the table holds two rows with id RC-2, so it is not salvaged (see the note on RC-2)",
				"node RC-4: the index entry for RC-4 points at the row of RC-2; " +
					"the table holds two rows with id RC-2, so it is not salvaged (see the note on RC-2)",
				"node RC-2: the table holds two rows with id RC-2: the row at rowid 2 is salvaged as RC-2, " +
					"and the row at rowid 3 is not salvaged; its content is ",
			},
			wantRecovered: []string{"RC-1", "RC-2"},
			wantMirror:    []string{"RC-3", "RC-4"},
			wantTitles: map[string]string{
				"RC-1": "reviewed task with a comment", "RC-2": "unaffected neighbour task",
				"RC-3": "third task", "RC-4": "fourth task",
			},
		},
		{
			name:       "two RC-2 entries point at the rows of RC-1 and RC-3 (RC-4's entry renamed); mirror lacks RC-2",
			rc4:        true,
			editMirror: dropRC2,
			patches: map[string]indexPatch{
				"RC-2": point("RC-1"), "RC-4": {renameTo: "RC-2", pointAt: "RC-3"},
			},
			wantNotes: []string{
				"node RC-2: the index entry for RC-2 points at the row of RC-1; " +
					"the entry of RC-1 reaches that row, so it is not salvaged again",
				"node RC-2: the index entry for RC-2 points at the row of RC-3; " +
					"the entry of RC-3 reaches that row, so it is not salvaged again",
				"1 row(s) were unreadable in the database and absent from the mirror",
			},
			wantRecovered: []string{"RC-1", "RC-3"},
			wantMirror:    []string{"RC-4"},
			wantLost:      []string{"RC-2"},
			wantTitles: map[string]string{
				"RC-1": "reviewed task with a comment", "RC-3": "third task", "RC-4": "fourth task",
			},
		},
		{
			name:       "a duplicated RC-2 entry that points at RC-1's row (RC-3's entry renamed); mirror lacks RC-2",
			editMirror: dropRC2,
			patches: map[string]indexPatch{
				"RC-2": point("RC-1"), "RC-3": {renameTo: "RC-2", pointAt: "RC-1"},
			},
			wantNotes: []string{
				"node RC-2: the index entry for RC-2 points at the row of RC-1; " +
					"the entry of RC-1 reaches that row, so it is not salvaged again",
				"1 row(s) were unreadable in the database and absent from the mirror",
			},
			wantRecovered: []string{"RC-1"},
			wantMirror:    []string{"RC-3"},
			wantLost:      []string{"RC-2"},
			wantTitles:    map[string]string{"RC-1": "reviewed task with a comment", "RC-3": "third task"},
		},
	}
	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			dbPath, mirrorPath := damagedIndexFixture(t, tc)

			res, err := Recover(context.Background(), dbPath, mirrorPath, "test-version", slog.Default())
			require.NoError(t, err)

			assertDamagedIndexSalvage(t, tc, res)
		})
	}
}

// assertDamagedIndexSalvage checks what recover reported for tc (MTIX-95.31.3).
func assertDamagedIndexSalvage(t *testing.T, tc *damagedIndexCase, res *RecoverResult) {
	t.Helper()
	for _, want := range tc.wantNotes {
		assert.Len(t, notesContaining(res.Notes, want), 1, "note %q; notes: %v", want, res.Notes)
	}
	var wantIndexNotes []string
	for _, want := range tc.wantNotes {
		if strings.Contains(want, indexEntryNoteText) {
			wantIndexNotes = append(wantIndexNotes, want)
		}
	}
	assert.Len(t, notesContaining(res.Notes, indexEntryNoteText), len(wantIndexNotes),
		"the index-entry notes are exactly the wanted ones; notes: %v", res.Notes)
	assert.Empty(t, notesContaining(res.Notes, "could not be read"), "no note claims a row could not be read")
	for _, n := range notesContaining(res.Notes, "index entry for") {
		assert.NotContains(t, n, "restored")
		assert.NotContains(t, n, "recovered")
	}
	assert.Equal(t, tc.wantRecovered, res.RecoveredIDs)
	assert.Equal(t, tc.wantMirror, res.FromMirror)
	assert.Equal(t, tc.wantLost, res.LostIDs)
	assert.Contains(t, columnNote(t, res, "RC-1", "annotations"), "restored", "RC-1's torn column is noted once")
	if tc.tearRC2 {
		assert.Contains(t, columnNote(t, res, "RC-2", "annotations"), "restored", "RC-2's torn column is noted once")
	}

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
	assert.Equal(t, tc.wantTitles, titles)
	rc1 := recoveredNode(t, res, "RC-1")
	require.Len(t, rc1.Annotations, 1, "RC-1's comment is restored once, on RC-1")
	assert.Equal(t, rcCommentID, rc1.Annotations[0].ID)

	fresh, err := New(filepath.Join(t.TempDir(), "fresh.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = fresh.Close() })
	_, err = fresh.Import(context.Background(), res.Export, ImportModeReplace, false)
	require.NoError(t, err, "the salvage file imports with a replace import")
}

// TestRecover_TableDuplicateRowWithTornColumn_NoteNamesTheColumn covers the
// unreadable-column part of the duplicate-row note (MTIX-107.78): when the row
// that is not salvaged has a JSON column that does not parse, the one note for
// that row names the column and the cause, after its content.
func TestRecover_TableDuplicateRowWithTornColumn_NoteNamesTheColumn(t *testing.T) {
	tc := &damagedIndexCase{
		patches:    map[string]indexPatch{"RC-3": {renameTo: "RC-2"}},
		renameRows: map[string]string{"RC-3": "RC-2"},
		tearRC3:    true,
	}
	dbPath, mirrorPath := damagedIndexFixture(t, tc)

	res, err := Recover(context.Background(), dbPath, mirrorPath, "test-version", slog.Default())
	require.NoError(t, err)

	dup := notesContaining(res.Notes, "the table holds two rows with id RC-2")
	require.Len(t, dup, 1, "notes: %v", res.Notes)
	assert.Contains(t, dup[0], "the row at rowid 3 is not salvaged; its content is ")
	assert.Contains(t, dup[0], "; its annotations column could not be read: ")
	assert.Equal(t, []string{"RC-1", "RC-2"}, res.RecoveredIDs, "the id is listed once")
}

// TestRecover_TableDuplicateRows_ReportPreservesContent checks the complete
// second row, rather than just its title, for both index paths (MTIX-107.78).
func TestRecover_TableDuplicateRows_ReportPreservesContent(t *testing.T) {
	for _, ownEntry := range []bool{true, false} {
		t.Run(fmt.Sprintf("own_entry=%t", ownEntry), func(t *testing.T) {
			tc := &damagedIndexCase{
				patches: map[string]indexPatch{}, renameRows: map[string]string{"RC-3": "RC-2"},
				seedContent: seedDuplicateReportContent,
			}
			if ownEntry {
				tc.patches["RC-3"] = indexPatch{renameTo: "RC-2"}
			}
			dbPath, mirrorPath := damagedIndexFixture(t, tc)
			db, err := sql.Open("sqlite", dbPath+"?mode=ro")
			require.NoError(t, err)
			second, err := readSalvageRow(context.Background(), db, salvageKey{rowid: 3})
			require.NoError(t, err)
			require.NoError(t, db.Close())
			requireDuplicateReportContent(t, second.node)

			res, err := Recover(context.Background(), dbPath, mirrorPath, "test-version", slog.Default())
			require.NoError(t, err)
			notes := notesContaining(res.Notes, "the row at rowid 3 is not salvaged; its content is ")
			require.Len(t, notes, 1)
			assert.Contains(t, notes[0], "the table holds two rows with id RC-2: the row at rowid 2 is salvaged")
			_, content, ok := strings.Cut(notes[0], "its content is ")
			require.True(t, ok)
			var reported exportNode
			require.NoError(t, json.Unmarshal([]byte(content), &reported))
			assert.Equal(t, second.node, reported, "every readable field of the second row survives in the report")
			assert.Equal(t, []string{"RC-1", "RC-2"}, res.RecoveredIDs)
			assert.Equal(t, "unaffected neighbour task", recoveredNode(t, res, "RC-2").Title)
			fresh, err := New(filepath.Join(t.TempDir(), "fresh.db"), slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, fresh.Close()) })
			_, err = fresh.Import(context.Background(), res.Export, ImportModeReplace, false)
			require.NoError(t, err)
		})
	}
}

// seedDuplicateReportContent makes lost scalar and JSON fields observable.
func seedDuplicateReportContent(t *testing.T, s *Store) {
	t.Helper()
	result, err := s.WriteDB().ExecContext(context.Background(),
		`UPDATE nodes SET depth = ?, parent_id = ?, agent_state = ?, node_type = ?,
		description = ?, prompt = ?, acceptance = ?, labels = ?,
		assignee = ?, creator = ?, progress = ?, estimate_min = ?, actual_min = ?,
		annotations = ?, code_refs = ?, commit_refs = ?, activity = ?, metadata = ? WHERE id = ?`,
		1, "RC-1", "idle", string(model.NodeTypeForDepth(1)),
		"discarded description", "discarded prompt", "discarded acceptance", `["recovery","duplicate"]`,
		"recovery-agent", "fixture-author", 25, 60, 15,
		`[{"id":"01J9RCCOMMENT0000000000001","author":"reviewer","text":"discarded review","created_at":"2026-09-01T10:00:00Z"}]`,
		`[{"file":"internal/store/sqlite/recover.go","line":71,"function":"Recover"}]`,
		`["abc1234"]`,
		`[{"id":"01J9RCACTIVITY000000000001","type":"comment","author":"reviewer","text":"discarded activity","created_at":"2026-09-01T10:00:00Z"}]`,
		`{"source":"duplicate-row","keep":true}`, "RC-3")
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
}

func requireDuplicateReportContent(t *testing.T, n exportNode) {
	t.Helper()
	require.Equal(t, 1, n.Depth)
	require.Equal(t, "RC-1", n.ParentID)
	require.Equal(t, "idle", n.AgentState)
	require.Equal(t, "discarded description", n.Description)
	require.Equal(t, "discarded prompt", n.Prompt)
	require.Equal(t, "discarded acceptance", n.Acceptance)
	require.NotEmpty(t, n.Labels)
	require.Equal(t, "recovery-agent", n.Assignee)
	require.Equal(t, "fixture-author", n.Creator)
	require.Equal(t, float64(25), n.Progress)
	require.NotNil(t, n.EstimateMin)
	require.NotNil(t, n.ActualMin)
	require.Len(t, n.Annotations, 1)
	require.Len(t, n.CodeRefs, 1)
	require.Len(t, n.CommitRefs, 1)
	require.Len(t, n.Activity, 1)
	require.JSONEq(t, `{"source":"duplicate-row","keep":true}`, n.Metadata)
}
