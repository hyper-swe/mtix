// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// A prior interrupted open may have left a regular file at the directory's
// path. Refuse that state rather than treating it as the board database.
func TestEnrolGuard_RegularDataFile_IsRefusedAndPreserved(t *testing.T) {
	testutil.OperatorState(t)
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".mtix"), 0o700))
	data := filepath.Join(root, ".mtix", "data")
	body := []byte("retained data file")
	require.NoError(t, os.WriteFile(data, body, 0o600))
	err := productionGlueDrain(t.Context(), attach.GlueDecision{Act: true, BoardRoot: root, StoreRoot: root})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	after, err := os.ReadFile(data)
	require.NoError(t, err)
	assert.Equal(t, body, after)
}
