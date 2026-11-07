// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// linkShape lays out links toward a database named real.db and returns the
// path to open and the physical directory that holds (or would hold) real.db.
type linkShape struct {
	name  string
	build func(t *testing.T) (open, realDir string)
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(p, 0o700))
}

func mustLink(t *testing.T, target, link string) {
	t.Helper()
	require.NoError(t, os.Symlink(target, link))
}

// chainShape links l0 -> l1 -> ... -> real.db inside one directory with
// relative targets, hops links long.
func chainShape(t *testing.T, hops int) (string, string) {
	dir := t.TempDir()
	for i := 0; i < hops; i++ {
		next := fmt.Sprintf("l%d.db", i+1)
		if i == hops-1 {
			next = "real.db"
		}
		mustLink(t, next, filepath.Join(dir, fmt.Sprintf("l%d.db", i)))
	}
	return filepath.Join(dir, "l0.db"), dir
}

// linkShapes are the ways a database link reaches its target. Every shape
// ends at real.db in the returned directory.
func linkShapes() []linkShape {
	return []linkShape{
		{"absolute", func(t *testing.T) (string, string) {
			dir := t.TempDir()
			mustLink(t, filepath.Join(dir, "real.db"), filepath.Join(dir, "link.db"))
			return filepath.Join(dir, "link.db"), dir
		}},
		{"relative", func(t *testing.T) (string, string) {
			dir := t.TempDir()
			mustLink(t, "real.db", filepath.Join(dir, "link.db"))
			return filepath.Join(dir, "link.db"), dir
		}},
		{"relative-dotdot", func(t *testing.T) (string, string) {
			dir := t.TempDir()
			mustMkdir(t, filepath.Join(dir, "data"))
			mustLink(t, filepath.Join("..", "real.db"), filepath.Join(dir, "data", "link.db"))
			return filepath.Join(dir, "data", "link.db"), dir
		}},
		{"two-hop-absolute-then-relative", func(t *testing.T) (string, string) {
			dir := t.TempDir()
			mustLink(t, "real.db", filepath.Join(dir, "hop.db"))
			mustLink(t, filepath.Join(dir, "hop.db"), filepath.Join(dir, "link.db"))
			return filepath.Join(dir, "link.db"), dir
		}},
		{"linked-data-directory-dotdot", func(t *testing.T) (string, string) {
			root, far := t.TempDir(), filepath.Join(t.TempDir(), "far", "a", "b")
			mustMkdir(t, filepath.Join(far, "data"))
			mustLink(t, filepath.Join(far, "data"), filepath.Join(root, "data"))
			mustLink(t, filepath.Join("..", "real.db"), filepath.Join(far, "data", "mtix.db"))
			return filepath.Join(root, "data", "mtix.db"), far
		}},
		{"two-hop-through-linked-directory", func(t *testing.T) (string, string) {
			root, far := t.TempDir(), filepath.Join(t.TempDir(), "far", "a", "b")
			mustMkdir(t, filepath.Join(far, "data"))
			mustLink(t, filepath.Join(far, "data"), filepath.Join(root, "data"))
			mustLink(t, "mid.db", filepath.Join(far, "data", "mtix.db"))
			mustLink(t, filepath.Join("..", "real.db"), filepath.Join(far, "data", "mid.db"))
			return filepath.Join(root, "data", "mtix.db"), far
		}},
		{"relative-path-from-working-directory", func(t *testing.T) (string, string) {
			dir := t.TempDir()
			mustMkdir(t, filepath.Join(dir, "sub"))
			mustLink(t, filepath.Join("..", "real.db"), filepath.Join(dir, "sub", "link.db"))
			t.Chdir(dir)
			return filepath.Join("sub", "link.db"), dir
		}},
		{"chain-of-4", func(t *testing.T) (string, string) { return chainShape(t, 4) }},
		{"chain-of-45", func(t *testing.T) (string, string) { return chainShape(t, 45) }},
	}
}

func skipWithoutSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require Windows privileges")
	}
}

// NFR-2.8: a dangling link must be walked the way the kernel and SQLite walk
// it: a relative target, a chain, a ".." taken after a linked directory, and
// 45 hops all end at the same real.db, so the WAL beside it is protected.
func TestNew_DanglingLinkShapes_RefuseAndPreserveWAL(t *testing.T) {
	skipWithoutSymlinks(t)
	for _, shape := range linkShapes() {
		for _, skip := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/skip-integrity=%t", shape.name, skip), func(t *testing.T) {
				if skip {
					t.Setenv("MTIX_SKIP_INTEGRITY_CHECK", "1")
				}
				source, _, _, _ := crashMailWAL(t)
				image := sourceImage(t, source)
				open, realDir := shape.build(t)
				wal, shm := filepath.Join(realDir, "real.db-wal"), filepath.Join(realDir, "real.db-shm")
				require.NoError(t, os.WriteFile(wal, image["-wal"], 0o600))
				require.NoError(t, os.WriteFile(shm, image["-shm"], 0o600))
				store, err := sqlite.New(open, slog.New(slog.NewTextHandler(io.Discard, nil)))
				if store != nil {
					require.NoError(t, store.Close())
				}
				require.ErrorIs(t, err, model.ErrCorrupted)
				_, statErr := os.Stat(filepath.Join(realDir, "real.db"))
				require.True(t, os.IsNotExist(statErr), "must not create the link target")
				gotWAL, readErr := os.ReadFile(wal)
				require.NoError(t, readErr)
				require.Equal(t, image["-wal"], gotWAL)
				gotSHM, readErr := os.ReadFile(shm)
				require.NoError(t, readErr)
				require.Equal(t, image["-shm"], gotSHM)
			})
		}
	}
}

// A real database reached through the same links is a healthy store: the
// stricter walk must not turn any of them into a refusal.
func TestNew_HealthyStoreThroughLinkShapes_IsNotRefused(t *testing.T) {
	skipWithoutSymlinks(t)
	for _, shape := range linkShapes() {
		if shape.name == "chain-of-45" {
			continue // the kernel itself stops at 40 hops for an existing file
		}
		t.Run(shape.name, func(t *testing.T) {
			source, _, _, _ := crashMailWAL(t)
			image := sourceImage(t, source)
			open, realDir := shape.build(t)
			for suffix, raw := range image {
				require.NoError(t, os.WriteFile(filepath.Join(realDir, "real.db"+suffix), raw, 0o600))
			}
			store, err := sqlite.New(open, slog.New(slog.NewTextHandler(io.Discard, nil)))
			require.NoError(t, err)
			require.NoError(t, store.Close())
		})
	}
}

// A link cycle can never be opened; the WAL beside the link must survive and
// the walk must give up with an error rather than loop.
func TestNew_LinkCycle_FailsWithWALIntact(t *testing.T) {
	skipWithoutSymlinks(t)
	source, _, _, _ := crashMailWAL(t)
	image := sourceImage(t, source)
	dir := t.TempDir()
	mustLink(t, "b.db", filepath.Join(dir, "a.db"))
	mustLink(t, "a.db", filepath.Join(dir, "b.db"))
	wal := filepath.Join(dir, "a.db-wal")
	require.NoError(t, os.WriteFile(wal, image["-wal"], 0o600))
	store, err := sqlite.New(filepath.Join(dir, "a.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if store != nil {
		require.NoError(t, store.Close())
	}
	require.Error(t, err)
	got, readErr := os.ReadFile(wal)
	require.NoError(t, readErr)
	require.Equal(t, image["-wal"], got)
}

// Past the hop cap the walk cannot say where SQLite will land, so it refuses.
func TestNew_LinkChainPastHopCap_IsRefused(t *testing.T) {
	skipWithoutSymlinks(t)
	open, _ := chainShape(t, 600)
	store, err := sqlite.New(open, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if store != nil {
		require.NoError(t, store.Close())
	}
	require.ErrorIs(t, err, model.ErrCorrupted)
}
