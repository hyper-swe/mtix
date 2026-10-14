// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Permission failures must propagate rather than silently rewriting user docs.
func TestProjectFiles_UnreadableAndUnwritable_ReturnErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc")
	require.NoError(t, os.WriteFile(path, []byte("safe"), 0600))
	require.NoError(t, os.Chmod(path, 0000))
	t.Cleanup(func() { require.NoError(t, os.Chmod(path, 0600)) })
	_, _, err := readRegularDoc(path)
	require.Error(t, err)
	require.Error(t, writeRegularDoc(path, []byte("new")))
}
