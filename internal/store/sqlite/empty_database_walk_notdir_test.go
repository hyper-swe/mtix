// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// A path below a regular file makes lstat fail with ENOTDIR. SQLite cannot
// open such a path, so no WAL can be lost through it: the walk must return
// the remaining parts joined and lexically cleaned (so "f/child/.." comes
// back as "f"), with a nil error, not refuse.
func TestResolveDatabasePath_BelowRegularFile_ReturnsPathWithoutError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ENOTDIR from lstat below a file is a Unix behaviour")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600))

	cases := []struct {
		name string
		open string
		want string
	}{
		{name: "file/child", open: "f/child", want: "f/child"},
		{name: "file/child/db", open: "f/child/db", want: "f/child/db"},
		{name: "file/child/..", open: "f/child/..", want: "f"},
		{name: "file/child/../db", open: "f/child/../db", want: "f/db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDatabasePath(dir + "/" + tc.open)
			require.NoError(t, err)
			require.Equal(t, filepath.Join(dir, tc.want), got)
		})
	}
}
