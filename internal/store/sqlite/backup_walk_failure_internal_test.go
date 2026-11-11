// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

const staleWALText = "stale WAL beside"

// NFR-2.8: when the walk of a backup destination cannot finish, mtix does not
// know which file the destination names, so the refusal must say that and
// must not claim a stale WAL sits beside it. A real stale WAL beside an empty
// destination keeps the stale-WAL advice.
func TestGuardBackupDestinationWAL_RefusalAdvice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require Windows privileges")
	}
	tests := []struct {
		name        string
		build       func(t *testing.T) string
		walkFailure bool
	}{
		{"symlink-loop", func(t *testing.T) string {
			dir := t.TempDir()
			require.NoError(t, os.Symlink("b.db", filepath.Join(dir, "a.db")))
			require.NoError(t, os.Symlink("a.db", filepath.Join(dir, "b.db")))
			return filepath.Join(dir, "a.db")
		}, true},
		{"chain-past-hop-cap", func(t *testing.T) string {
			dir := t.TempDir()
			hops := maxLinkHops + 1
			for i := 0; i < hops; i++ {
				next := fmt.Sprintf("l%d.db", i+1)
				if i == hops-1 {
					next = "real.db"
				}
				require.NoError(t, os.Symlink(next, filepath.Join(dir, fmt.Sprintf("l%d.db", i))))
			}
			return filepath.Join(dir, "l0.db")
		}, true},
		{"unreadable-component", func(t *testing.T) string {
			if os.Geteuid() == 0 {
				t.Skip("root reads any directory")
			}
			locked := filepath.Join(t.TempDir(), "locked")
			require.NoError(t, os.Mkdir(locked, 0o700))
			require.NoError(t, os.Mkdir(filepath.Join(locked, "sub"), 0o700))
			require.NoError(t, os.Chmod(locked, 0))
			t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
			return filepath.Join(locked, "sub", "backup.db")
		}, true},
		{"stale-wal-beside-empty", func(t *testing.T) string {
			dest := filepath.Join(t.TempDir(), "backup.db")
			require.NoError(t, os.WriteFile(dest, nil, 0o600))
			require.NoError(t, os.WriteFile(dest+"-wal", []byte("stale"), 0o600))
			return dest
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := guardBackupDestinationWAL(tt.build(t))
			require.ErrorIs(t, err, model.ErrCorrupted)
			msg := err.Error()
			if tt.walkFailure {
				require.Contains(t, msg, backupWalkFailureAdvice)
				require.Contains(t, msg, "cannot tell which file the destination names")
				require.Contains(t, msg, "choose another destination")
				require.NotContains(t, msg, staleWALText)
				return
			}
			require.Contains(t, msg, backupRefusalAdvice)
			require.Contains(t, msg, staleWALText)
		})
	}
}
