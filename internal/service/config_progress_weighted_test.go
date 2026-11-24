// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProgressWeighted_DefaultOffAndFollowsFile verifies the FR-5.8 switch is
// off by default and a later `config set` is seen without a restart.
func TestProgressWeighted_DefaultOffAndFollowsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cs, err := NewConfigService(path)
	require.NoError(t, err)
	assert.False(t, cs.ProgressWeighted(), "default is the FR-5.3 count rollup")

	other, err := NewConfigService(path)
	require.NoError(t, err)
	_, err = other.Set("progress.weighted", "true")
	require.NoError(t, err)
	assert.True(t, cs.ProgressWeighted(), "a running process follows the file")
}
