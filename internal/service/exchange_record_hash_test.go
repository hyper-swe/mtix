// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Independent received byte and form fixtures pin provenance and task identity.
package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

const exchangeCurrentJSON = "{\"version\":1,\"schema_version\":\"2.0.0\",\"exported_at\":\"2026-10-01T01:00:00Z\",\"mtix_version\":\"reference\",\"project\":\"TEST\",\"nodes\":[{\"id\":\"TEST-1\",\"parent_id\":\"\",\"depth\":0,\"seq\":1,\"project\":\"TEST\",\"title\":\"Received task\",\"description\":\"\",\"prompt\":\"\",\"acceptance\":\"\",\"node_type\":\"story\",\"issue_type\":\"\",\"priority\":3,\"labels\":\"[]\",\"status\":\"open\",\"progress\":0,\"assignee\":\"\",\"creator\":\"test-agent\",\"agent_state\":\"\",\"weight\":1,\"content_hash\":\"\",\"created_at\":\"2026-10-01T00:00:00Z\",\"updated_at\":\"2026-10-01T00:00:00Z\",\"uid\":\"0199e5bc-7c00-7000-8000-000000000001\",\"metadata\":\"{\\\"note\\\":\\\"carried metadata\\\"}\"}],\"dependencies\":null,\"agents\":null,\"sessions\":null,\"node_count\":1,\"checksum\":\"b9da7fec7fe5b0cd45ef9475c34944cbaa001f92e78c125b16cabb220c22e222\"}"
const exchangeLegacyJSON = "{\"version\":1,\"schema_version\":\"1.0.0\",\"exported_at\":\"2026-10-01T01:00:00Z\",\"mtix_version\":\"reference\",\"project\":\"TEST\",\"nodes\":[{\"id\":\"TEST-1\",\"parent_id\":\"\",\"depth\":0,\"seq\":1,\"project\":\"TEST\",\"title\":\"Received task\",\"description\":\"\",\"prompt\":\"\",\"acceptance\":\"\",\"node_type\":\"story\",\"issue_type\":\"\",\"priority\":3,\"labels\":\"[]\",\"status\":\"open\",\"progress\":0,\"assignee\":\"\",\"creator\":\"test-agent\",\"agent_state\":\"\",\"weight\":1,\"content_hash\":\"\",\"created_at\":\"2026-10-01T00:00:00Z\",\"updated_at\":\"2026-10-01T00:00:00Z\"}],\"dependencies\":null,\"agents\":null,\"sessions\":null,\"node_count\":1,\"checksum\":\"2c7466bffce827c1f6c72872e52bc5b31356eb8b71493a515d1bc2fa182343ab\"}"
const exchangeSchema1ProjectionJSON = "{\"version\":1,\"schema_version\":\"1.0.0\",\"exported_at\":\"2026-10-01T01:00:00Z\",\"mtix_version\":\"reference\",\"project\":\"TEST\",\"nodes\":[{\"id\":\"TEST-1\",\"parent_id\":\"\",\"depth\":0,\"seq\":1,\"project\":\"TEST\",\"title\":\"Received task\",\"description\":\"\",\"prompt\":\"\",\"acceptance\":\"\",\"node_type\":\"story\",\"issue_type\":\"\",\"priority\":3,\"labels\":\"[]\",\"status\":\"open\",\"progress\":0,\"assignee\":\"\",\"creator\":\"test-agent\",\"agent_state\":\"\",\"weight\":1,\"content_hash\":\"\",\"created_at\":\"2026-10-01T00:00:00Z\",\"updated_at\":\"2026-10-01T00:00:00Z\",\"metadata\":\"{\\\"note\\\":\\\"carried metadata\\\"}\"}],\"dependencies\":null,\"agents\":null,\"sessions\":null,\"node_count\":1,\"checksum\":\"a7a9c2b72659427bcc1e5a68730ead85484d3f1774b4dd6855ed6e358e02760c\"}"

func exchangeFixture(t *testing.T) (*SyncService, string) {
	t.Helper()
	if !exchangePlatformSupported() {
		t.Skip("receipt ownership requires a supported native platform")
	}
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o755))
	st, err := sqlite.New(filepath.Join(dir, "data", "mtix.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	clock := func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
	st.SetClock(clock)
	return NewSyncService(st, slog.Default(), clock), dir
}

func exchangeReference(t *testing.T, raw string) *sqlite.ExportData {
	t.Helper()
	data, err := sqlite.DecodeExportData(strings.NewReader(raw))
	require.NoError(t, err)
	require.NoError(t, sqlite.ValidateExport(data))
	return data
}

func exchangePublishReference(t *testing.T, s *SyncService, dir string) *ExchangeRecord {
	t.Helper()
	lock, err := s.acquireLock(dir, lockExclusive)
	require.NoError(t, err)
	defer s.releaseLock(lock)
	r, err := buildExchangeRecord(exchangeReference(t, exchangeCurrentJSON), "git-import", receivedDigest([]byte(exchangeCurrentJSON)), s.clock().Format(time.RFC3339))
	require.NoError(t, err)
	require.NoError(t, s.publishReceived(t.Context(), dir, r))
	return r
}

func TestExchangeRecord_ReferenceForms_IndependentDigests(t *testing.T) {
	for _, tc := range []struct{ name, raw, rawHash, boardHash, kind, taskHash string }{
		{"current", exchangeCurrentJSON, "2e414fb0ac5e4a43c1a110d1ab55709343d261063961487d54d89f9b21b784b7", "c9f695ea6ec28ab5cc4afc2b90326a66b0f4d9d26fad99f8cc0b50734f3e72bb", "uid", "04db0e150ba3b8a1c0977e8fd03e76674d76dd379029137607a086b32daf5986"},
		{"legacy", exchangeLegacyJSON, "4242fcf99b20dcb19aa307f1cb3f10eaa02069e891d43efaf787a5ca4a4e06aa", "ecca7c39ca3c2c71a89f885cdbd9a220f945a630d505a37ea28110edbdb6f8c3", "legacy-id", "a14ad674150bbd6f9687cfa08c484e57032f9394183e8076c906dabc97bab038"},
		{"historical projection", exchangeSchema1ProjectionJSON, "ba92f800ce6002553e1fe4cffd32bdf790420759e18897f2d1a15e1a7df9fd76", "ecca7c39ca3c2c71a89f885cdbd9a220f945a630d505a37ea28110edbdb6f8c3", "legacy-id", "a14ad674150bbd6f9687cfa08c484e57032f9394183e8076c906dabc97bab038"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := exchangeReference(t, tc.raw)
			before, err := json.Marshal(data)
			require.NoError(t, err)
			r, err := buildExchangeRecord(data, "git-import", receivedDigest([]byte(tc.raw)), "2026-10-01T00:00:00Z")
			require.NoError(t, err)
			require.Equal(t, tc.rawHash, r.BoardFileHash)
			require.Equal(t, tc.boardHash, r.BoardHash)
			require.Equal(t, tc.kind, r.Tasks[0].Kind)
			require.Equal(t, tc.taskHash, r.Tasks[0].Hash)
			require.Equal(t, "24b5a3f23a3bd4eb10a811214ee53e119f0f07fb049562020ca8002ceb77b06b", r.Tasks[0].Fields["title"])
			require.Equal(t, "398b676e47d03a29016ee92fe378b8b4f1b3e965390c4c64ce78d27f79df74d1", r.Tasks[0].Fields["activity"])
			after, err := json.Marshal(data)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestExchangeRecord_ReorderedNodes_OnlyTaskProjectionIsInvariant(t *testing.T) {
	data := exchangeReference(t, exchangeCurrentJSON)
	second := data.Nodes[0]
	second.ID = "TEST-2"
	second.UID = "0199e5bc-7c00-7000-8000-000000000002"
	data.Nodes = append(data.Nodes, second)
	data.NodeCount = 2
	a, err := buildExchangeRecord(data, "git-import", strings.Repeat("a", 64), "2026-10-01T00:00:00Z")
	require.NoError(t, err)
	data.Nodes[0], data.Nodes[1] = data.Nodes[1], data.Nodes[0]
	b, err := buildExchangeRecord(data, "git-import", strings.Repeat("a", 64), "2026-10-01T00:00:00Z")
	require.NoError(t, err)
	require.Equal(t, a.Tasks, b.Tasks)
	require.NotEqual(t, a.BoardHash, b.BoardHash)
}

func TestExchangeRecord_Fields_PresenceAndValuesAreBound(t *testing.T) {
	current, err := buildExchangeRecord(exchangeReference(t, exchangeCurrentJSON), "git-import", strings.Repeat("a", 64), "2026-10-01T00:00:00Z")
	require.NoError(t, err)
	legacy, err := buildExchangeRecord(exchangeReference(t, exchangeLegacyJSON), "git-import", strings.Repeat("a", 64), "2026-10-01T00:00:00Z")
	require.NoError(t, err)
	require.NotEqual(t, current.Tasks[0].Fields["metadata"], legacy.Tasks[0].Fields["metadata"])
	for _, key := range exchangeFields() {
		fields := map[string]json.RawMessage{key: json.RawMessage(`"first"`)}
		before := hashExchangeFields(fields)
		fields[key] = json.RawMessage(`"second"`)
		after := hashExchangeFields(fields)
		for _, other := range exchangeFields() {
			if other == key {
				require.NotEqual(t, before[other], after[other])
			} else {
				require.Equal(t, before[other], after[other])
			}
		}
	}
}

func TestExchangeRecord_AmbiguousIdentityAndUnknownForm_Refused(t *testing.T) {
	data := exchangeReference(t, exchangeCurrentJSON)
	data.Nodes = append(data.Nodes, data.Nodes[0])
	_, err := buildExchangeRecord(data, "git-import", strings.Repeat("a", 64), "2026-10-01T00:00:00Z")
	require.ErrorIs(t, err, ErrNoExchangeRecord)
	data.SchemaVersion = "3.0.0"
	_, err = buildExchangeRecord(data, "git-import", strings.Repeat("a", 64), "2026-10-01T00:00:00Z")
	require.ErrorIs(t, err, ErrNoExchangeRecord)
}

func TestExchangeRecord_RepeatedSameBoard_PriorProvenanceRemainsValid(t *testing.T) {
	s, dir := exchangeFixture(t)
	first := exchangePublishReference(t, s, dir)
	second := exchangePublishReference(t, s, dir)
	require.Equal(t, first.BoardHash, second.BoardHash)
	require.Empty(t, second.ParentBoardHash, "same-board association is omitted by writer convention")
	loaded, err := s.LastExchanged(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, second, loaded)
}

func TestExchangeRecord_CompatibleUnknownForm_ImportPreservesPriorReceipt(t *testing.T) {
	for _, version := range []string{"1.1.0", "1.99.0", "2.7.0", ""} {
		t.Run(version, func(t *testing.T) {
			s, dir := exchangeFixture(t)
			exchangePublishReference(t, s, dir)
			prior, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			raw := strings.Replace(exchangeCurrentJSON, `"schema_version":"2.0.0"`, `"schema_version":"`+version+`"`, 1)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(raw), 0o644))
			require.NoError(t, s.AutoImport(t.Context(), dir))
			node, err := s.store.GetNode(t.Context(), "TEST-1")
			require.NoError(t, err)
			require.Equal(t, "Received task", node.Title)
			after, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			require.Equal(t, prior, after)
			status, err := s.ExchangeRecordStatus(t.Context(), dir)
			require.NoError(t, err)
			require.True(t, status.Present)
			require.Equal(t, "unsupported-schema", status.Reason)
		})
	}
}

func TestExchangeRecord_CompatibleUnknownForm_MissingReceiptStaysMissing(t *testing.T) {
	s, dir := exchangeFixture(t)
	raw := strings.Replace(exchangeCurrentJSON, `"schema_version":"2.0.0"`, `"schema_version":"2.7.0"`, 1)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(raw), 0o644))
	require.NoError(t, s.AutoImport(t.Context(), dir))
	_, err := os.Stat(exchangePath(dir))
	require.ErrorIs(t, err, os.ErrNotExist)
	status, err := s.ExchangeRecordStatus(t.Context(), dir)
	require.NoError(t, err)
	require.False(t, status.Present)
	require.Equal(t, "unsupported-schema", status.Reason)
}

func TestExchangeRecord_CompatibleUnknownForm_ReasonFailureIsNotSuppressed(t *testing.T) {
	s, dir := exchangeFixture(t)
	data := exchangeReference(t, exchangeCurrentJSON)
	data.SchemaVersion = "2.7.0"
	require.NoError(t, s.store.Close())
	err := s.recordReceivedImport(t.Context(), dir, data, strings.Repeat("a", 64))
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNoExchangeRecord)
	require.Contains(t, err.Error(), "closed")
}
