// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// MTIX-187.7.15.1: an id that is not valid UTF-8 travels through the JSON
// export as an optional hex field, with a split checksum (checksum = what a
// v0.5.4 reader sees, ids_checksum = the hex fields).

// invalidUTF8Fixtures returns the HostileIDs fixtures that are not UTF-8.
func invalidUTF8Fixtures() []testutil.HostileFixture {
	var out []testutil.HostileFixture
	for _, fx := range testutil.HostileIDs() {
		if !utf8.ValidString(fx.Value) {
			out = append(out, fx)
		}
	}
	return out
}

// hostileExport builds a store with one node whose uid is id and (history
// on) the history row its create event wrote, carrying id as task_uid and event_id, and returns its export file bytes.
func hostileExport(t *testing.T, id string, history bool) (*sqlite.ExportData, []byte) {
	t.Helper()
	src, _ := hostileStore(t)
	require.NoError(t, hostileApply(t, src, hostileCreate(t, id, "MTIX-1", 1)))
	if history {
		// Applying the create event already wrote a history row whose
		// task_uid is the node uid and whose event_id is the event id.
		src.SetHistoryExportSource(func() bool { return true })
	}
	data, err := src.Export(t.Context(), "MTIX", "test")
	require.NoError(t, err)
	file, err := json.Marshal(data)
	require.NoError(t, err)
	return data, file
}

func TestExport_InvalidUTF8IDs_RoundTripExactThroughFile(t *testing.T) {
	fixtures := invalidUTF8Fixtures()
	require.NotEmpty(t, fixtures)
	for _, fx := range fixtures {
		t.Run(fx.Name, func(t *testing.T) {
			_, file := hostileExport(t, fx.Value, true)
			text := string(file)
			require.Contains(t, text, `"uid_hex":"`+hex.EncodeToString([]byte(fx.Value))+`"`)
			require.Contains(t, text, `"task_uid_hex"`)
			require.Contains(t, text, `"event_id_hex"`)
			require.Contains(t, text, `"ids_checksum"`)

			var fromFile sqlite.ExportData
			require.NoError(t, json.Unmarshal(file, &fromFile))
			require.Equal(t, fx.Value, fromFile.Nodes[0].UID)
			dst, dstRaw := hostileStore(t)
			_, err := dst.Import(t.Context(), &fromFile, sqlite.ImportModeReplace, true)
			require.NoError(t, err)
			require.Equal(t, [][]byte{[]byte(fx.Value)}, idsIn(t, dstRaw, `SELECT uid FROM nodes WHERE id = 'MTIX-1'`))
			for _, column := range []string{"task_uid", "event_id"} {
				got := idsIn(t, dstRaw, `SELECT `+column+` FROM task_history WHERE origin = 'hub'`)
				require.NotEmpty(t, got, column)
				for _, id := range got {
					require.Equal(t, []byte(fx.Value), id, column)
				}
			}
		})
	}
}

func TestExport_ValidUTF8IDs_FileCarriesNoHexFields(t *testing.T) {
	for _, id := range []string{"evt-plain", "evt\u202etxt", "evt\x00tail", "evt\ufffdx"} {
		_, file := hostileExport(t, id, true)
		for _, field := range []string{"uid_hex", "task_uid_hex", "event_id_hex", "ids_checksum"} {
			require.NotContains(t, string(file), field, "%q", id)
		}
		var back sqlite.ExportData
		require.NoError(t, json.Unmarshal(file, &back))
		ok, err := sqlite.VerifyExportChecksum(&back)
		require.NoError(t, err)
		require.True(t, ok, "%q", id)
	}
}

// tamper rewrites one JSON path of the export file with edit and returns the
// result decoded.
func tamper(t *testing.T, file []byte, edit func(doc map[string]any)) *sqlite.ExportData {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(file, &doc))
	edit(doc)
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal(out, &data))
	return &data
}

func firstNode(doc map[string]any) map[string]any {
	return doc["nodes"].([]any)[0].(map[string]any)
}

func TestVerifyExportChecksum_InvalidUTF8IDs_VerifiesAndDetectsTamper(t *testing.T) {
	fx := invalidUTF8Fixtures()[0]
	_, file := hostileExport(t, fx.Value, true)
	// Two invalid bytes, like the first fixture: the reader view (U+FFFD per
	// byte) is unchanged, so only ids_checksum can catch the edit.
	other := hex.EncodeToString([]byte("evt\xfe\xff"))

	var intact sqlite.ExportData
	require.NoError(t, json.Unmarshal(file, &intact))
	ok, err := sqlite.VerifyExportChecksum(&intact)
	require.NoError(t, err)
	require.True(t, ok, "an untouched export verifies")

	cases := map[string]func(doc map[string]any){
		"uid_hex":      func(doc map[string]any) { firstNode(doc)["uid_hex"] = other },
		"task_uid_hex": func(doc map[string]any) { history0(doc)["task_uid_hex"] = other },
		"event_id_hex": func(doc map[string]any) { history0(doc)["event_id_hex"] = other },
		"ids_checksum": func(doc map[string]any) { delete(doc, "ids_checksum") },
		"uid_hex_gone": func(doc map[string]any) { delete(firstNode(doc), "uid_hex") },
		"uid_hex_utf8": func(doc map[string]any) { firstNode(doc)["uid_hex"] = hex.EncodeToString([]byte("abc")) },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			data := tamper(t, file, edit)
			ok, err := sqlite.VerifyExportChecksum(data)
			require.NoError(t, err)
			require.False(t, ok)
			dst, _ := hostileStore(t)
			_, err = dst.Import(t.Context(), data, sqlite.ImportModeReplace, true)
			require.Error(t, err)
		})
	}
}

// history0 returns the first history entry of the first node.
func history0(doc map[string]any) map[string]any {
	return firstNode(doc)["history"].([]any)[0].(map[string]any)
}

func TestExportNodeJSON_HexWinsOverPlainAndBadHexIsRejected(t *testing.T) {
	raw := "evt\x80"
	var data sqlite.ExportData
	doc := `{"nodes":[{"id":"A","uid":"plain","uid_hex":"` + hex.EncodeToString([]byte(raw)) + `"}]}`
	require.NoError(t, json.Unmarshal([]byte(doc), &data))
	require.Equal(t, raw, data.Nodes[0].UID)

	for _, bad := range []string{"zz", "abc"} {
		var d sqlite.ExportData
		err := json.Unmarshal([]byte(`{"nodes":[{"id":"A","uid_hex":"`+bad+`"}]}`), &d)
		require.ErrorIs(t, err, model.ErrInvalidInput, bad)
		err = json.Unmarshal([]byte(`{"history":[{"id":"h","event_id_hex":"`+bad+`"}]}`), &d)
		require.ErrorIs(t, err, model.ErrInvalidInput, bad)
	}
}

// TestExport_InvalidUTF8UID_RealV054ReaderGivesFreshUID runs the real tagged
// v0.5.4 binary on a 2.0.0 export (history.export off) holding a hex field:
// the file imports, that one node gets a fresh uid, and every other column
// and node is unchanged. v0.5.4 refuses every 2.1.0 (history) file, with or
// without hex fields, so history files need a current reader.
func TestExport_InvalidUTF8UID_RealV054ReaderGivesFreshUID(t *testing.T) {
	reader := compat054Binary(t)
	for _, fx := range invalidUTF8Fixtures() {
		t.Run(fx.Name, func(t *testing.T) {
			src, srcRaw := hostileStore(t)
			require.NoError(t, hostileApply(t, src, hostileCreate(t, fx.Value, "MTIX-1", 1)))
			require.NoError(t, hostileApply(t, src, hostileCreate(t, "evt-ok", "MTIX-2", 2)))
			data, err := src.Export(t.Context(), "MTIX", "test")
			require.NoError(t, err)
			file, err := json.Marshal(data)
			require.NoError(t, err)
			require.True(t, strings.Contains(string(file), "uid_hex"))

			project := t.TempDir()
			readerDir := filepath.Join(project, ".mtix")
			require.NoError(t, os.MkdirAll(filepath.Join(readerDir, "data"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(readerDir, "config.yaml"),
				[]byte("prefix: MTIX\nsync:\n  auto_sync: false\n"), 0o600))
			incoming := filepath.Join(project, "incoming.json")
			require.NoError(t, os.WriteFile(incoming, file, 0o600))
			attachReaderCommand(t, reader, project, "", "import", incoming)

			db := openReaderDB(t, readerDir)
			uids := readerUIDs(t, db)
			require.Len(t, uids, 2)
			require.NotEmpty(t, uids["MTIX-1"], "the reader gives the node a fresh uid")
			require.NotEqual(t, fx.Value, uids["MTIX-1"])
			replaced, err := json.Marshal(fx.Value)
			require.NoError(t, err)
			var replacement string
			require.NoError(t, json.Unmarshal(replaced, &replacement))
			require.NotEqual(t, replacement, uids["MTIX-1"], "not the replacement-character identity")
			require.Equal(t, "evt-ok", uids["MTIX-2"], "every other node is unchanged")
			require.Equal(t, nodeColumns(t, srcRaw), nodeColumns(t, db), "every column but uid is unchanged")
		})
	}
}

// openReaderDB opens the board database the old reader wrote, read-only in
// effect: the test only selects.
func openReaderDB(t *testing.T, readerDir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(readerDir, "data", "mtix.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// TestExportForms_InvalidUTF8UID_StillVerify covers the places that rebuild
// an export: the 1.0.0 form drops the hex field, WithoutUIDs drops the uid and
// its ids_checksum, and a recomputed checksum covers the hex fields again.
func TestExportForms_InvalidUTF8UID_StillVerify(t *testing.T) {
	fx := invalidUTF8Fixtures()[0]
	data, _ := hostileExport(t, fx.Value, true)

	form, err := sqlite.Schema100Form(data)
	require.NoError(t, err)
	formFile, err := json.Marshal(form)
	require.NoError(t, err)
	require.NotContains(t, string(formFile), "_hex")
	// Byte parity with 0.5.3: the uid keeps JSON's U+FFFD spelling.
	spelled, err := json.Marshal(fx.Value)
	require.NoError(t, err)
	var replaced string
	require.NoError(t, json.Unmarshal(spelled, &replaced))
	require.Equal(t, replaced, back0UID(t, formFile))
	var back sqlite.ExportData
	require.NoError(t, json.Unmarshal(formFile, &back))
	ok, err := sqlite.VerifyExportChecksum(&back)
	require.NoError(t, err)
	require.True(t, ok, "1.0.0 form")

	without, err := sqlite.WithoutUIDs(data)
	require.NoError(t, err)
	require.NotEqual(t, data.IDsChecksum, without.IDsChecksum, "the uid left the ids; the history ids remain")
	require.NotEmpty(t, without.IDsChecksum)
	ok, err = sqlite.VerifyExportChecksum(without)
	require.NoError(t, err)
	require.True(t, ok, "WithoutUIDs")

	data.Checksum, data.IDsChecksum = "stale", "stale"
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	ok, err = sqlite.VerifyExportChecksum(data)
	require.NoError(t, err)
	require.True(t, ok, "RecomputeExportChecksum")
	require.NotEmpty(t, data.IDsChecksum)
}

// readerUIDs returns the uid of every node the old reader stored, by id.
func readerUIDs(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT id, uid FROM nodes ORDER BY id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	uids := map[string]string{}
	for rows.Next() {
		var id, uid string
		require.NoError(t, rows.Scan(&id, &uid))
		uids[id] = uid
	}
	require.NoError(t, rows.Err())
	return uids
}

// nodeColumns returns every schema-1.0.0 column except uid of each node, by
// id, as text.
func nodeColumns(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT id, parent_id, depth, seq, project, title, description,
		prompt, acceptance, node_type, issue_type, priority, labels, status, progress, assignee, creator,
		agent_state, weight, content_hash, created_at, updated_at, closed_at, defer_until, deleted_at
		FROM nodes ORDER BY id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	out := map[string]string{}
	for rows.Next() {
		cols := make([]any, 25)
		ptrs := make([]any, len(cols))
		for i := range cols {
			ptrs[i] = &cols[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		out[fmt.Sprint(cols[0])] = fmt.Sprintf("%#v", cols)
	}
	require.NoError(t, rows.Err())
	return out
}

// orphanExport is hostileExport (history on) plus a history entry of a task
// that no longer exists, with its own invalid-UTF-8 task_uid and event_id; it
// travels in the file's top-level history list.
func orphanExport(t *testing.T, id string) []byte {
	t.Helper()
	src, raw := hostileStore(t)
	require.NoError(t, hostileApply(t, src, hostileCreate(t, id, "MTIX-1", 1)))
	_, err := raw.Exec(`INSERT INTO task_history(entry_id, task_uid, ts, actor, origin, kind, field, event_id)
		VALUES('01ORPHAN00000000000000000A', ?, '2026-10-28T10:00:00.000000000Z', 'a', 'hub', 'change', 'title', ?)`,
		"gone\xfe\xff", "ev\xfe\xff")
	require.NoError(t, err)
	src.SetHistoryExportSource(func() bool { return true })
	data, err := src.Export(t.Context(), "MTIX", "test")
	require.NoError(t, err)
	require.Len(t, data.History, 1, "the orphan travels in the top-level list")
	file, err := json.Marshal(data)
	require.NoError(t, err)
	return file
}

// stripHex removes every *_hex field and ids_checksum from a decoded file.
func stripHex(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if strings.HasSuffix(k, "_hex") || k == "ids_checksum" {
				delete(x, k)
				continue
			}
			stripHex(child)
		}
	case []any:
		for _, child := range x {
			stripHex(child)
		}
	}
}

// TestVerifyExportChecksum_HistoryHexFields_NotCoveredByChecksum pins the
// split: with every hex field and ids_checksum removed, the file is what a
// v0.5.4 reader sees and its checksum still verifies, for node-attached and
// orphan history alike.
func TestVerifyExportChecksum_HistoryHexFields_NotCoveredByChecksum(t *testing.T) {
	file := orphanExport(t, invalidUTF8Fixtures()[0].Value)
	require.Contains(t, string(file), "task_uid_hex")
	require.Contains(t, string(file), "event_id_hex")
	data := tamper(t, file, func(doc map[string]any) { stripHex(doc) })
	require.NotContains(t, mustJSON(t, data), "_hex")
	ok, err := sqlite.VerifyExportChecksum(data)
	require.NoError(t, err)
	require.True(t, ok, "checksum covers exactly the reader view")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// TestVerifyExportChecksum_OrphanHistoryHexTamper_Fails: ids_checksum covers
// the hex fields of history entries that belong to no node.
func TestVerifyExportChecksum_OrphanHistoryHexTamper_Fails(t *testing.T) {
	file := orphanExport(t, invalidUTF8Fixtures()[0].Value)
	var intact sqlite.ExportData
	require.NoError(t, json.Unmarshal(file, &intact))
	ok, err := sqlite.VerifyExportChecksum(&intact)
	require.NoError(t, err)
	require.True(t, ok)
	// Two invalid bytes like the originals: only ids_checksum can see it.
	edits := map[string]string{
		"task_uid_hex": hex.EncodeToString([]byte("gone\xfd\xfe")),
		"event_id_hex": hex.EncodeToString([]byte("ev\xfd\xfe")),
	}
	for field, other := range edits {
		t.Run(field, func(t *testing.T) {
			data := tamper(t, file, func(doc map[string]any) {
				doc["history"].([]any)[0].(map[string]any)[field] = other
			})
			ok, err := sqlite.VerifyExportChecksum(data)
			require.NoError(t, err)
			require.False(t, ok)
		})
	}
}

// back0UID returns the uid of the first node of an export file, as decoded.
func back0UID(t *testing.T, file []byte) string {
	t.Helper()
	var doc struct {
		Nodes []struct {
			UID string `json:"uid"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(file, &doc))
	return doc.Nodes[0].UID
}
