// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-2.3.2.1: the staged export hashes to exactly what the historical
// reader hashes, for every shape of file the reader accepts. Written red-first.
package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// historicalDigests runs the checksums the way the current reader does:
// decode the whole file, then hash the decoded slices.
func historicalDigests(t *testing.T, raw []byte) StagedChecksums {
	t.Helper()
	data, err := DecodeExportData(bytes.NewReader(raw))
	require.NoError(t, err)
	sum, err := computeExportChecksumH(data.Nodes, data.Dependencies, data.History)
	require.NoError(t, err)
	legacy, err := legacyReplacementChecksumH(data.Nodes, data.Dependencies, data.History)
	require.NoError(t, err)
	ids, err := computeIDsChecksum(data.Nodes, data.History)
	require.NoError(t, err)
	return StagedChecksums{Checksum: sum, Legacy: legacy, IDs: ids}
}

func stagedDigests(t *testing.T, raw []byte) (*StagedExport, StagedChecksums) {
	t.Helper()
	st, err := StageExport(context.Background(), bytes.NewReader(raw), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	got, err := st.Checksums(context.Background())
	require.NoError(t, err)
	return st, got
}

func stageNode(id, title string) exportNode {
	return exportNode{ID: id, Project: "STG", Title: title, Status: "open", NodeType: "epic",
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z", Weight: 1,
		Labels: "[]"}
}

func stageHistoryEntry(id, uid, event string) exportHistoryEntry {
	return exportHistoryEntry{ID: id, TaskUID: uid, TS: "2026-01-01T00:00:00Z", Actor: "a",
		Origin: "cli", Kind: "title", Field: "title", EventID: event}
}

// stageFixtures are export documents of every shape the checksum treats
// differently: null versus empty arrays, received order, orphan and embedded
// history, invalid UTF-8 and U+FFFD text, hex ids.
func stageFixtures() map[string]*ExportData {
	badUID := stageNode("STG-2", "bad uid")
	badUID.UID = "uid\xffbyte"
	badUID.History = []exportHistoryEntry{stageHistoryEntry("h1", "t\xfe1", "e\xff1")}
	both := stageNode("STG-3", "genuine � and bad \xc3")
	both.Description = "truncated \xe2\x80"
	annotated := stageNode("STG-4", "plain")
	annotated.CodeRefs = []model.CodeRef{{File: "a.go"}}
	annotated.CommitRefs = []string{}
	return map[string]*ExportData{
		"nil arrays":      {NodeCount: 0},
		"empty arrays":    {Nodes: []exportNode{}, Dependencies: []exportDep{}, History: []exportHistoryEntry{}},
		"plain":           {NodeCount: 2, Nodes: []exportNode{stageNode("STG-1", "a"), annotated}},
		"received order":  {NodeCount: 2, Nodes: []exportNode{stageNode("STG-9", "z"), stageNode("STG-1", "a")}},
		"invalid uid":     {NodeCount: 1, Nodes: []exportNode{badUID}},
		"replacement mix": {NodeCount: 1, Nodes: []exportNode{both}},
		"deps unordered": {NodeCount: 1, Nodes: []exportNode{stageNode("STG-1", "a")},
			Dependencies: []exportDep{{FromID: "STG-2", ToID: "STG-1", DepType: "blocks"}, {FromID: "STG-1", ToID: "STG-2", DepType: "blocks"}}},
		"orphan history": {NodeCount: 1, Nodes: []exportNode{stageNode("STG-1", "a")},
			History: []exportHistoryEntry{stageHistoryEntry("o2", "u\xff", "e2"), stageHistoryEntry("o1", "u1", "e\xfe")}},
	}
}

func TestStageExport_ChecksumsMatchHistoricalReader(t *testing.T) {
	for name, doc := range stageFixtures() {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(doc)
			require.NoError(t, err)
			_, got := stagedDigests(t, raw)
			assert.Equal(t, historicalDigests(t, raw), got)
		})
	}
}

func TestStageExport_ChecksumsMatchTextVariants(t *testing.T) {
	node := `{"id":"A-1","title":"t\ud800x","uid_hex":"ff00ff","history":[{"id":"h","task_uid":"u","event_id_hex":"fe"}]}`
	variants := map[string]string{
		"missing keys":      `{"version":1}`,
		"top-level null":    `null`,
		"null nodes":        `{"nodes":null,"dependencies":null,"history":null}`,
		"upper-case keys":   `{"NODES":[` + node + `],"Dependencies":[],"HISTORY":[{"id":"o"}]}`,
		"duplicate nodes":   `{"nodes":[{"id":"x"},{"id":"y"}],"nodes":[` + node + `]}`,
		"duplicate to null": `{"nodes":[{"id":"x"}],"nodes":null}`,
		"null element":      `{"nodes":[null,{"id":"B"}],"dependencies":[null]}`,
		"unknown keys":      `{"zzz":{"a":[1,2]},"nodes":[{"id":"B","unknown":1}]}`,
		"hex wins":          `{"nodes":[` + node + `]}`,
	}
	for name, text := range variants {
		t.Run(name, func(t *testing.T) {
			raw := []byte(text)
			st, got := stagedDigests(t, raw)
			assert.Equal(t, historicalDigests(t, raw), got)
			old, err := DecodeExportData(bytes.NewReader(raw))
			require.NoError(t, err)
			assert.Equal(t, len(old.Nodes), st.NodeCount())
		})
	}
}

func TestStageExport_HeaderMatchesHistoricalDecode(t *testing.T) {
	raw := []byte(`{"version":2,"schema_version":"2.0.0","project":"P","node_count":1,` +
		`"checksum":"c","ids_checksum":"i","agents":[{"agent_id":"a"}],"sessions":[],"nodes":[{"id":"x"}]}`)
	st, _ := stagedDigests(t, raw)
	old, err := DecodeExportData(bytes.NewReader(raw))
	require.NoError(t, err)
	old.Nodes = nil
	assert.Equal(t, *old, st.Header)
}

func TestStageExport_VerifyAgreesWithVerifyExportChecksum(t *testing.T) {
	for name, doc := range stageFixtures() {
		t.Run(name, func(t *testing.T) {
			doc.NodeCount = len(doc.Nodes)
			sum, err := computeExportChecksumH(doc.Nodes, doc.Dependencies, doc.History)
			require.NoError(t, err)
			doc.Checksum = sum
			doc.IDsChecksum, err = computeIDsChecksum(doc.Nodes, doc.History)
			require.NoError(t, err)
			raw, err := json.Marshal(doc)
			require.NoError(t, err)
			st, _ := stagedDigests(t, raw)
			require.NoError(t, st.Verify(context.Background()))

			tampered := bytes.Replace(raw, []byte(`"project":"STG"`), []byte(`"project":"EVIL"`), 1)
			if bytes.Equal(tampered, raw) {
				return
			}
			bad, _ := stagedDigests(t, tampered)
			assert.ErrorIs(t, bad.Verify(context.Background()), model.ErrInvalidInput)
		})
	}
}

func TestStageExport_VerifyAcceptsLegacyReplacementDigest(t *testing.T) {
	doc := stageFixtures()["replacement mix"]
	legacy, err := legacyReplacementChecksumH(doc.Nodes, doc.Dependencies, doc.History)
	require.NoError(t, err)
	require.NotEmpty(t, legacy)
	doc.Checksum, doc.NodeCount = legacy, 1
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	old, err := DecodeExportData(bytes.NewReader(raw))
	require.NoError(t, err)
	ok, err := VerifyExportChecksum(old)
	require.NoError(t, err)
	require.True(t, ok, "the historical reader accepts the legacy digest")
	st, _ := stagedDigests(t, raw)
	assert.NoError(t, st.Verify(context.Background()))
}

func TestStageExport_VerifyRejectsNodeCountMismatch(t *testing.T) {
	st, _ := stagedDigests(t, []byte(`{"node_count":3,"nodes":[{"id":"x"}]}`))
	assert.ErrorIs(t, st.Verify(context.Background()), model.ErrInvalidInput)
}

func TestStageExport_RejectsWhatTheHistoricalReaderRejects(t *testing.T) {
	for name, text := range map[string]string{
		"trailing object":  `{"nodes":[]} {"extra":true}`,
		"truncated":        `{"nodes":[{"id":"x"}`,
		"not an object":    `[1,2]`,
		"nodes not array":  `{"nodes":"x"}`,
		"bad element type": `{"nodes":[{"id":5}]}`,
		"bad hex":          `{"nodes":[{"id":"x","uid_hex":"zz"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, oldErr := DecodeExportData(strings.NewReader(text))
			require.Error(t, oldErr, "fixture must be rejected today")
			st, err := StageExport(context.Background(), strings.NewReader(text), t.TempDir())
			require.Error(t, err)
			assert.Nil(t, st)
		})
	}
}

func TestStageExport_AcceptsTrailingWhitespace(t *testing.T) {
	st, _ := stagedDigests(t, []byte("{\"nodes\":[]}\n\t "))
	assert.Equal(t, 0, st.NodeCount())
}

func TestStageExport_GoldenFilesMatchHistoricalReader(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			require.NoError(t, err)
			if _, decErr := DecodeExportData(bytes.NewReader(raw)); decErr != nil {
				t.Skip("not an export document")
			}
			st, got := stagedDigests(t, raw)
			assert.Equal(t, historicalDigests(t, raw), got)
			old, _ := DecodeExportData(bytes.NewReader(raw))
			valid, err := VerifyExportChecksum(old)
			require.NoError(t, err)
			assert.Equal(t, valid && old.NodeCount == len(old.Nodes), st.Verify(context.Background()) == nil)
		})
	}
}

func TestStageExport_CloseRemovesStagingFiles(t *testing.T) {
	st, err := StageExport(context.Background(), strings.NewReader(`{"nodes":[{"id":"x"}]}`), "")
	require.NoError(t, err)
	dir := st.dir
	require.DirExists(t, dir)
	require.NoError(t, st.Close())
	assert.NoDirExists(t, dir)
}
