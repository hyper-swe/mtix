// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// The released reader's config writer must preserve the separate committed identity.

import (
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestBoardID_ConfigSetBy054_FileIntact(t *testing.T) {
	reader := compat054Binary(t)
	project := t.TempDir()
	config, err := service.NewConfigService("")
	require.NoError(t, err)
	require.NoError(t, config.InitConfig(project, "TEST"))
	_, err = config.Set("sync.auto_sync", "false")
	require.NoError(t, err)
	dir := filepath.Join(project, ".mtix")
	_, err = service.CreateBoardID(dir)
	require.NoError(t, err)
	path := filepath.Join(dir, "board-id")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Len(t, before, 37)
	attachReaderCommand(t, reader, project, "", "config", "set", "progress.weighted", "true")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	changed, err := service.NewConfigService(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	value, err := changed.Get("progress.weighted")
	require.NoError(t, err)
	assert.Equal(t, "true", value)
}
