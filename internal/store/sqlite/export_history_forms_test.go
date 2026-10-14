// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestVerifyExportChecksum_Format210WithOrphan_LegacyReplacementSpellingVerifies:
// the 0.5.3 spelling of invalid UTF-8 (each U+FFFD as the escape �) is
// accepted for a 2.1.0 file too, and covers the top-level history list.
func TestVerifyExportChecksum_Format210WithOrphan_LegacyReplacementSpellingVerifies(t *testing.T) {
	data, err := sqlite.DecodeExportData(strings.NewReader(writtenBy053WithInvalidUTF8))
	require.NoError(t, err)
	setOrphan(t, data, "bad\ufffdactor")

	doc := struct {
		Nodes   any `json:"nodes"`
		Deps    any `json:"deps"`
		History any `json:"history,omitempty"`
	}{data.Nodes, data.Dependencies, data.History}
	canonical, err := json.Marshal(doc)
	require.NoError(t, err)
	spelled := strings.ReplaceAll(string(canonical), "\ufffd", "\\ufffd")
	data.Checksum = fmt.Sprintf("%x", sha256.Sum256([]byte(spelled)))

	require.NotEqual(t, string(canonical), spelled, "the test changes real bytes")
	require.Contains(t, spelled, `\ufffd`)
	ok, err := sqlite.VerifyExportChecksum(data)
	require.NoError(t, err)
	require.True(t, ok, "the legacy spelling covers the history list")
	data.History[0].Field = "priority"
	ok, err = sqlite.VerifyExportChecksum(data)
	require.NoError(t, err)
	require.False(t, ok)
}

// TestOlderBaselineForms_Format210WithOrphan_KeepHistoryInTheirChecksum: the
// forms without uids carry the history list and a checksum that covers it.
func TestOlderBaselineForms_Format210WithOrphan_KeepHistoryInTheirChecksum(t *testing.T) {
	_, file := historyFile(t)
	require.NotEmpty(t, file.History)
	for name, form := range map[string]func(*sqlite.ExportData) (*sqlite.ExportData, error){
		"without backfill uids": sqlite.WithoutBackfillUIDs,
		"without uids":          sqlite.WithoutUIDs,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := form(file)
			require.NoError(t, err)
			require.Equal(t, file.History, got.History)
			ok, err := sqlite.VerifyExportChecksum(got)
			require.NoError(t, err)
			require.True(t, ok)
			got.History[0].Actor = name + "-tampered"
			ok, err = sqlite.VerifyExportChecksum(got)
			require.NoError(t, err)
			require.False(t, ok, "the form's checksum covers the history")
		})
	}
}

// setOrphan gives the export one valid orphan entry with the given actor.
func setOrphan(t *testing.T, data *sqlite.ExportData, actor string) {
	t.Helper()
	entry := []map[string]string{{"id": "01H0000000000000000000Z001", "task_uid": "ghost-uid",
		"ts": "2026-03-14T12:00:00.000000000Z", "actor": actor, "origin": "local", "kind": "change", "field": "status"}}
	raw, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &data.History))
}
