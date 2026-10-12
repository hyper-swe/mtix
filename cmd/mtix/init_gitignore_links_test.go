// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAddToGitignore_NonRegularTarget_RefusesWithoutWrite(t *testing.T) {
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			outside := filepath.Join(t.TempDir(), "operator-data")
			require.NoError(t, os.WriteFile(outside, []byte("operator data"), 0600))
			path := filepath.Join(dir, ".gitignore")
			if kind == "symlink" {
				if err := os.Symlink(outside, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else {
				require.NoError(t, os.Mkdir(path, 0700))
			}
			require.Error(t, addToGitignore(dir, ".mtix/hooks.trust"))
			got, err := os.ReadFile(outside)
			require.NoError(t, err)
			require.Equal(t, "operator data", string(got))
		})
	}
}
