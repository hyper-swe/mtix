// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-2.3.2.1 round 2: Verify's ids_checksum branch, and the staged recompute
// that recovery and rewritten checksums use. Written red-first.
package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// sealedExport returns the fixture as a file text whose node_count, checksum
// and ids_checksum are correct.
func sealedExport(t *testing.T, name string) []byte {
	t.Helper()
	doc := stageFixtures()[name]
	doc.NodeCount = len(doc.Nodes)
	sum, err := computeExportChecksumH(doc.Nodes, doc.Dependencies, doc.History)
	require.NoError(t, err)
	doc.Checksum = sum
	doc.IDsChecksum, err = computeIDsChecksum(doc.Nodes, doc.History)
	require.NoError(t, err)
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	return raw
}

// Each case changes only what ids_checksum covers (or the ids_checksum field
// itself): the main checksum stays valid, so only the ids_checksum branch of
// Verify can reject the file.
func TestStageExport_Verify_IDsChecksumBranchRejectsWhatHistoricalReaderRejects(t *testing.T) {
	cases := []struct {
		name, fixture string
		edit          func(t *testing.T, raw []byte) []byte
	}{
		{"altered uid_hex", "invalid uid", func(t *testing.T, raw []byte) []byte {
			return replaceOnce(t, raw, `"uid_hex":"756964ff62797465"`, `"uid_hex":"756964fe62797465"`)
		}},
		{"altered task_uid_hex", "invalid uid", func(t *testing.T, raw []byte) []byte {
			return replaceOnce(t, raw, `"task_uid_hex":"74fe31"`, `"task_uid_hex":"74fd31"`)
		}},
		{"altered event_id_hex", "orphan history", func(t *testing.T, raw []byte) []byte {
			return replaceOnce(t, raw, `"event_id_hex":"65fe"`, `"event_id_hex":"65fd"`)
		}},
		{"altered orphan task_uid_hex", "orphan history", func(t *testing.T, raw []byte) []byte {
			return replaceOnce(t, raw, `"task_uid_hex":"75ff"`, `"task_uid_hex":"75fe"`)
		}},
		{"ids_checksum dropped", "invalid uid", func(t *testing.T, raw []byte) []byte {
			return dropIDsChecksum(t, raw)
		}},
		{"ids_checksum added where no id is invalid", "plain", func(t *testing.T, raw []byte) []byte {
			return replaceOnce(t, raw, `"checksum":`, `"ids_checksum":"00ff","checksum":`)
		}},
		{"ids_checksum changed", "orphan history", func(t *testing.T, raw []byte) []byte {
			var doc map[string]any
			require.NoError(t, json.Unmarshal(raw, &doc))
			doc["ids_checksum"] = "00ff"
			out, err := json.Marshal(doc)
			require.NoError(t, err)
			return out
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.edit(t, sealedExport(t, tc.fixture))
			old, err := DecodeExportData(bytes.NewReader(raw))
			require.NoError(t, err)
			sum, err := computeExportChecksumH(old.Nodes, old.Dependencies, old.History)
			require.NoError(t, err)
			require.Equal(t, old.Checksum, sum, "the main checksum must stay valid in this case")
			require.Equal(t, old.NodeCount, len(old.Nodes))
			valid, err := VerifyExportChecksum(old)
			require.NoError(t, err)
			require.False(t, valid, "the historical reader rejects the file")

			st, _ := stagedDigests(t, raw)
			assert.ErrorIs(t, st.Verify(context.Background()), model.ErrInvalidInput)
		})
	}
}

func replaceOnce(t *testing.T, raw []byte, from, to string) []byte {
	t.Helper()
	require.Equal(t, 1, bytes.Count(raw, []byte(from)), "fixture must hold %s once", from)
	return bytes.Replace(raw, []byte(from), []byte(to), 1)
}

func dropIDsChecksum(t *testing.T, raw []byte) []byte {
	t.Helper()
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Contains(t, doc, "ids_checksum")
	delete(doc, "ids_checksum")
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	return out
}

// The staged recompute is RecomputeExportChecksum without the decoded board:
// the same node_count, checksum and ids_checksum for every fixture, whatever
// order the file lists the elements in.
func TestStageExport_Recompute_EqualsRecomputeExportChecksum(t *testing.T) {
	fixtures := stageFixtures()
	fixtures["history sorted by task time id"] = &ExportData{
		Nodes: []exportNode{stageNode("STG-2", "b"), stageNode("STG-1", "a")},
		History: []exportHistoryEntry{
			{ID: "h3", TaskUID: "u2", TS: "2026-01-01T00:00:00Z", Actor: "a", Origin: "cli", Kind: "title", Field: "title"},
			{ID: "h2", TaskUID: "u1", TS: "2026-01-02T00:00:00Z", Actor: "a", Origin: "cli", Kind: "title", Field: "title", EventID: "e\xff"},
			{ID: "h1", TaskUID: "u1", TS: "2026-01-01T00:00:00Z", Actor: "a", Origin: "cli", Kind: "title", Field: "title"},
			{ID: "h0", TaskUID: "u1", TS: "2026-01-01T00:00:00Z", Actor: "a", Origin: "cli", Kind: "title", Field: "title"},
		},
		Dependencies: []exportDep{{FromID: "STG-2", ToID: "STG-1"}, {FromID: "STG-1", ToID: "STG-3"}, {FromID: "STG-1", ToID: "STG-2"}},
	}
	for name, doc := range fixtures {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(doc)
			require.NoError(t, err)
			st, _ := stagedDigests(t, raw)

			whole, err := DecodeExportData(bytes.NewReader(raw))
			require.NoError(t, err)
			require.NoError(t, RecomputeExportChecksum(whole))

			got, err := st.Recompute(context.Background())
			require.NoError(t, err)
			assert.Equal(t, StagedRecompute{NodeCount: whole.NodeCount, Checksum: whole.Checksum, IDs: whole.IDsChecksum}, got)
		})
	}
}

func TestStageExport_Recompute_RewritesAStaleChecksumLikeRecovery(t *testing.T) {
	raw := []byte(`{"node_count":9,"checksum":"stale","nodes":[{"id":"B-2","title":"b"},{"id":"B-1","title":"a"}],` +
		`"dependencies":[{"from_id":"B-2","to_id":"B-1"},{"from_id":"B-1","to_id":"B-2"}]}`)
	st, _ := stagedDigests(t, raw)
	require.ErrorIs(t, st.Verify(context.Background()), model.ErrInvalidInput)

	whole, err := DecodeExportData(bytes.NewReader(raw))
	require.NoError(t, err)
	require.NoError(t, RecomputeExportChecksum(whole))
	got, err := st.Recompute(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, got.NodeCount)
	assert.Equal(t, whole.Checksum, got.Checksum)
	assert.NotEqual(t, "stale", got.Checksum)
}
