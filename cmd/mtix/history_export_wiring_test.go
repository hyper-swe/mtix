// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInitApp_HistoryExportSwitch_DrivesTheStoreExport: the store opened by
// initApp follows the history.export config key, both ways and without a
// restart, and records this binary's version as the last writer
// (MTIX-95.31.19.5).
func TestInitApp_HistoryExportSwitch_DrivesTheStoreExport(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()

	off, err := app.store.Export(ctx, "", "")
	require.NoError(t, err)
	require.Equal(t, "2.0.0", off.SchemaVersion, "default: off")
	require.Empty(t, off.MtixVersion)

	_, err = app.configSvc.Set("history.export", "true")
	require.NoError(t, err)
	on, err := app.store.Export(ctx, "", "")
	require.NoError(t, err)
	require.Equal(t, "2.1.0", on.SchemaVersion)
	require.Equal(t, version, on.MtixVersion, "the binary's version is the last writer")

	_, err = app.configSvc.Set("history.export", "false")
	require.NoError(t, err)
	back, err := app.store.Export(ctx, "", "")
	require.NoError(t, err)
	require.Equal(t, "2.0.0", back.SchemaVersion)
}
