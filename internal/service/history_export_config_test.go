// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestConfig_HistoryExport_DefaultOffAndBoolean: history.export is a known
// key, off by default, and accepts only true or false.
func TestConfig_HistoryExport_DefaultOffAndBoolean(t *testing.T) {
	cs, err := service.NewConfigService(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, err)
	require.False(t, cs.HistoryExport())
	_, err = cs.Set("history.export", "true")
	require.NoError(t, err)
	require.True(t, cs.HistoryExport())
	_, err = cs.Set("history.export", "maybe")
	require.Error(t, err)
	require.True(t, cs.HistoryExport(), "a refused value changes nothing")
}

// TestCheckSchemaVersion_AcceptsFormat210AndStill200: the history format is a
// minor bump, so readers of this build accept 2.1.0 and 2.0.0 and refuse 3.x.
func TestCheckSchemaVersion_AcceptsFormat210AndStill200(t *testing.T) {
	for _, v := range []string{"2.1.0", "2.0.0", "1.0.0", ""} {
		require.NoError(t, service.CheckSchemaVersion(v), v)
	}
	require.Error(t, service.CheckSchemaVersion("3.0.0"))
	require.Equal(t, "2.1.0", sqlite.SchemaVersionHistory)
}
