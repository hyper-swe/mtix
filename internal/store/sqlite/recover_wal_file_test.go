// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// FR-19.1: checking an unverifiable FIFO must return without waiting for a writer.
func TestRecoverWAL_FileAvailability_FIFO(t *testing.T) {
	if path := os.Getenv("MTIX_TEST_RECOVER_WAL_FIFO"); path != "" {
		require.ErrorContains(t, checkRecoverWAL(t.Context(), path), "regular file")
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix filesystem FIFO")
	}
	path := filepath.Join(t.TempDir(), "source.db")
	output, err := exec.Command("mkfifo", path+"-wal").CombinedOutput()
	require.NoError(t, err, string(output))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoverWAL_FileAvailability_FIFO$")
	cmd.Env = append(os.Environ(), "MTIX_TEST_RECOVER_WAL_FIFO="+path)
	output, err = cmd.CombinedOutput()
	require.NoError(t, ctx.Err(), "WAL FIFO must not block: %s", output)
	require.NoError(t, err, string(output))
}

// FR-19.1: a regular but unreadable WAL cannot be treated as absent.
func TestRecoverWAL_FileAvailability_Unreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	require.NoError(t, os.WriteFile(path+"-wal", walFormatVector(t, false), 0600))
	require.NoError(t, os.Chmod(path+"-wal", 0))
	t.Cleanup(func() { _ = os.Chmod(path+"-wal", 0600) })
	if f, err := os.Open(path + "-wal"); err == nil {
		require.NoError(t, f.Close())
		t.Skip("host privileges bypass file read permissions")
	}
	require.ErrorContains(t, checkRecoverWAL(t.Context(), path), "open WAL")
}

type walChangingContext struct {
	context.Context
	once   sync.Once
	change func()
}

// Err is a deterministic walk callback: metadata has been captured, but the
// first frame has not yet been read. No timing race or production hook is needed.
func (c *walChangingContext) Err() error { c.once.Do(c.change); return c.Context.Err() }

// FR-19.1: compare path identity, size and mtime after the bounded walk.
func TestRecoverWAL_ChangedDuringVerification_IsUnproven(t *testing.T) {
	for _, mode := range []string{"mtime", "size", "replacement", "removed"} {
		t.Run(mode, func(t *testing.T) {
			if runtime.GOOS == "windows" && (mode == "replacement" || mode == "removed") {
				t.Skip("Windows disallows replacing an open file")
			}
			path := filepath.Join(t.TempDir(), "source.db")
			data := walFormatVector(t, false)
			require.NoError(t, os.WriteFile(path+"-wal", data, 0600))
			before, err := os.Stat(path + "-wal")
			require.NoError(t, err)
			changed := false
			ctx := &walChangingContext{Context: t.Context(), change: func() {
				changed = true
				switch mode {
				case "mtime":
					require.NoError(t, os.Chtimes(path+"-wal", before.ModTime(), before.ModTime().Add(time.Second)))
				case "size":
					require.NoError(t, os.WriteFile(path+"-wal", append(data, 0), 0600))
					require.NoError(t, os.Chtimes(path+"-wal", before.ModTime(), before.ModTime()))
				case "replacement":
					replacement := path + "-replacement"
					require.NoError(t, os.WriteFile(replacement, data, 0600))
					require.NoError(t, os.Chtimes(replacement, before.ModTime(), before.ModTime()))
					require.NoError(t, os.Rename(replacement, path+"-wal"))
				case "removed":
					require.NoError(t, os.Remove(path+"-wal"))
				}
			}}
			err = checkRecoverWAL(ctx, path)
			require.True(t, changed, "the change hook must execute during the walk")
			if mode == "removed" {
				require.ErrorContains(t, err, "recheck WAL")
			} else {
				require.ErrorContains(t, err, "WAL changed during verification")
			}
		})
	}
}
