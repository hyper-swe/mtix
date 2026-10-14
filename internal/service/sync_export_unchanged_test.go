// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestMatchingPublishedExport_RejectsUnusableDiskContent(t *testing.T) {
	current := &sqlite.ExportData{Version: 1, SchemaVersion: sqlite.SchemaVersionV1}
	require.NoError(t, sqlite.RecomputeExportChecksum(current))
	require.NoError(t, sqlite.ValidateExport(current))
	valid, err := json.Marshal(current)
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"malformed", "{broken"},
		{"older schema", `{"version":1,"schema_version":"1.0.0"}`},
		{"invalid checksum", `{"version":1,"schema_version":"2.0.0","checksum":"invalid"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tasks.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.raw), 0o644))
			got, readErr := matchingPublishedExport(path, current)
			require.NoError(t, readErr)
			require.Nil(t, got, "unusable files cannot authorize a skipped publication")
		})
	}
	t.Run("missing", func(t *testing.T) {
		got, readErr := matchingPublishedExport(filepath.Join(t.TempDir(), "tasks.json"), current)
		require.NoError(t, readErr)
		require.Nil(t, got)
	})
	t.Run("unreadable", func(t *testing.T) {
		got, readErr := matchingPublishedExport(t.TempDir(), current)
		require.ErrorContains(t, readErr, "read published export")
		require.Nil(t, got)
	})
	t.Run("non timestamp field changed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tasks.json")
		require.NoError(t, os.WriteFile(path, valid, 0o644))
		changed := *current
		changed.Project = "different project"
		got, readErr := matchingPublishedExport(path, &changed)
		require.NoError(t, readErr)
		require.Nil(t, got, "comparison must include export fields beyond node checksum")
	})
}
