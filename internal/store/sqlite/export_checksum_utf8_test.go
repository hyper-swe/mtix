// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-107.39, folded into MTIX-95.31.1 (FR-7.8, FR-15.2): a
// tasks.json written by mtix always verifies on import, including a copy
// restored from git. Root cause: a stored text holding invalid UTF-8.
// 0.5.3 hashed the first JSON encoding of the export, in which each invalid
// byte appears as the escape \ufffd. A reader decodes that escape to U+FFFD
// and re-encodes U+FFFD as the raw character, so its encoding differed from
// the one hashed and the checksum never matched. Written red-first.
package sqlite_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// seedInvalidUTF8Store creates nodes whose texts hold invalid UTF-8 (a
// stray byte, a truncated multi-byte character, two bad bytes in a row, a
// genuine U+FFFD next to a bad byte) and an annotation with a bad byte.
func seedInvalidUTF8Store(t *testing.T) *sqlite.Store {
	t.Helper()
	ctx := context.Background()
	s := newTestStore(t)
	ts := columnTestTime()
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "UTF-1", Project: "UTF", Depth: 0, Seq: 1, Title: "bad\xffbyte title",
		Description: "truncated dash \xe2\x80", Prompt: "two\xff\xfebytes",
		Acceptance: "genuine \ufffd then bad \xc3", Labels: []string{"l\xffabel"},
		NodeType: model.NodeTypeEpic, Priority: model.PriorityMedium,
		Status: model.StatusOpen, Weight: 1.0, ContentHash: "hash-utf-1",
		CreatedAt: ts, UpdatedAt: ts,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "UTF-2", Project: "UTF", Depth: 0, Seq: 2, Title: "clean",
		NodeType: model.NodeTypeEpic, Priority: model.PriorityMedium,
		Status: model.StatusOpen, Weight: 1.0, ContentHash: "hash-utf-2",
		CreatedAt: ts, UpdatedAt: ts,
	}))
	require.NoError(t, s.SetAnnotations(ctx, "UTF-2", []model.Annotation{{
		ID: "01J9ANNOTATION0000000000UT", Author: "agent\xff", Text: "comment \xf0\x9f",
		CreatedAt: ts,
	}}))
	return s
}

// TestExport_InvalidUTF8Text_WrittenFileVerifiesAndImports verifies that a
// file mtix writes for a store holding invalid UTF-8 verifies after it is
// read back by both readers (auto-import's json.Unmarshal and mtix
// import's streaming decoder) and imports in both modes (MTIX-107.39).
func TestExport_InvalidUTF8Text_WrittenFileVerifiesAndImports(t *testing.T) {
	ctx := context.Background()
	src := seedInvalidUTF8Store(t)
	data, err := src.Export(ctx, "", "")
	require.NoError(t, err)
	file, err := json.MarshalIndent(data, "", "  ") // what AutoExport writes
	require.NoError(t, err)

	var unmarshaled sqlite.ExportData
	require.NoError(t, json.Unmarshal(file, &unmarshaled))
	streamed, err := sqlite.DecodeExportData(bytes.NewReader(file))
	require.NoError(t, err)

	for name, read := range map[string]*sqlite.ExportData{"auto-import": &unmarshaled, "mtix import": streamed} {
		valid, verifyErr := sqlite.VerifyExportChecksum(read)
		require.NoError(t, verifyErr)
		assert.True(t, valid, "%s: a file written by mtix must verify", name)
	}

	_, err = newTestStore(t).Import(ctx, streamed, sqlite.ImportModeReplace, false)
	require.NoError(t, err, "replace import of the file mtix wrote")
	_, err = src.Import(ctx, &unmarshaled, sqlite.ImportModeMerge, false)
	require.NoError(t, err, "merge import of the file mtix wrote")
}

// writtenBy053WithInvalidUTF8 is a tasks.json that the 0.5.3 code wrote for
// a scratch board whose SCR-1 title and SCR-2 prompt were given invalid
// UTF-8 (the MTIX-107.39 reproduction). 0.5.3 hashed its first encoding,
// with each invalid byte as the escape \ufffd; a reader re-encodes the
// decoded U+FFFD differently, so no 0.5.3 reader could verify the file.
const writtenBy053WithInvalidUTF8 = `{
  "version": 1,
  "schema_version": "1.0.0",
  "exported_at": "2026-09-24T11:07:43Z",
  "mtix_version": "",
  "project": "",
  "nodes": [
    {
      "id": "SCR-1", "parent_id": "", "depth": 0, "seq": 1, "project": "SCR",
      "title": "bad\ufffdbyte title", "description": "", "prompt": "p",
      "acceptance": "a", "node_type": "epic", "issue_type": "", "priority": 3,
      "labels": "[]", "status": "open", "progress": 0, "assignee": "",
      "creator": "cli", "agent_state": "", "weight": 1,
      "content_hash": "60d1ae43ffc721ece7f803800f58feae4b946740dbd748a21cd9bea70c1f0c56",
      "created_at": "2026-09-24T11:07:43Z", "updated_at": "2026-09-24T11:07:43Z",
      "uid": "01a0d319-9113-7802-b11e-e55104f9d879"
    },
    {
      "id": "SCR-2", "parent_id": "", "depth": 0, "seq": 2, "project": "SCR",
      "title": "clean", "description": "", "prompt": "p \ufffd x",
      "acceptance": "a", "node_type": "epic", "issue_type": "", "priority": 3,
      "labels": "[]", "status": "open", "progress": 0, "assignee": "",
      "creator": "cli", "agent_state": "", "weight": 1,
      "content_hash": "d67421acc6faf5a6a85bd0959f079d44f5b113f035ad4eb01838a3709c33372a",
      "created_at": "2026-09-24T11:07:43Z", "updated_at": "2026-09-24T11:07:43Z",
      "uid": "01a0d319-9123-7d0f-9a05-2969eba3c6ec"
    }
  ],
  "dependencies": null,
  "agents": null,
  "sessions": null,
  "node_count": 2,
  "checksum": "e50201ed038544a1a7e12da803f33647534457876c4046ee2640f0e1c6c8da57"
}`

// TestVerifyExportChecksum_FileWrittenBy053WithInvalidUTF8_Verifies verifies
// that a restored copy written before the fix, which every earlier reader
// rejected, now verifies: its checksum spelled each invalid byte as the
// escape \ufffd, and verification accepts that spelling of the same decoded
// content (MTIX-107.39).
func TestVerifyExportChecksum_FileWrittenBy053WithInvalidUTF8_Verifies(t *testing.T) {
	data, err := sqlite.DecodeExportData(strings.NewReader(writtenBy053WithInvalidUTF8))
	require.NoError(t, err)
	valid, err := sqlite.VerifyExportChecksum(data)
	require.NoError(t, err)
	assert.True(t, valid)

	_, err = newTestStore(t).Import(context.Background(), data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
}

// TestVerifyExportChecksum_EditedInvalidUTF8File_ReturnsFalse verifies that
// accepting the earlier spelling does not weaken integrity: an edit to any
// text of such a file still fails verification (MTIX-107.39, FR-7.8).
func TestVerifyExportChecksum_EditedInvalidUTF8File_ReturnsFalse(t *testing.T) {
	tests := []struct {
		name, from, to string
	}{
		{"text next to the replaced byte", "bad\\ufffdbyte title", "bad\\ufffdbyte titlE"},
		{"replacement removed", "p \\ufffd x", "p  x"},
		{"replacement doubled", "p \\ufffd x", "p \\ufffd\\ufffd x"},
		{"clean title", `"title": "clean"`, `"title": "clean!"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			edited := strings.Replace(writtenBy053WithInvalidUTF8, tt.from, tt.to, 1)
			require.NotEqual(t, writtenBy053WithInvalidUTF8, edited)
			data, err := sqlite.DecodeExportData(strings.NewReader(edited))
			require.NoError(t, err)
			valid, err := sqlite.VerifyExportChecksum(data)
			require.NoError(t, err)
			assert.False(t, valid)
		})
	}
}
