// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// walRemovingFile removes the WAL (failing the test if it cannot) right after its second Stat, modelling a
// checkpoint that completes between the size measurement and a later WAL
// observation.
type walRemovingFile struct {
	*os.File
	walPath string
	calls   int
	t       *testing.T
}

func (f *walRemovingFile) Stat() (os.FileInfo, error) {
	info, err := f.File.Stat()
	f.calls++
	if f.calls >= 2 {
		// A failed removal would let the reversed order pass silently.
		if rmErr := os.Remove(f.walPath); rmErr != nil {
			f.t.Fatalf("remove WAL to model a finishing checkpoint: %v", rmErr)
		}
	}
	return info, err
}

// TestValidateOpenDBFile_WALObservedBeforeSize_DefersToWAL pins the ordering
// the MTIX-107.109 fix depends on: a non-empty WAL is observed BEFORE the
// size is measured. Measuring first and observing the WAL later would pair a
// small size with a WAL a finishing checkpoint has just removed.
func TestValidateOpenDBFile_WALObservedBeforeSize_DefersToWAL(t *testing.T) {
	s, dbPath := newDurabilityTestStore(t)
	seedRows(t, s)
	require.NoError(t, s.Close())
	doctorHeaderPageCount(t, dbPath, 1<<20)
	require.NoError(t, os.WriteFile(dbPath+"-wal", []byte("frames"), 0o600))

	f, err := os.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })

	require.NoError(t, validateOpenDBFile(dbPath, &walRemovingFile{File: f, walPath: dbPath + "-wal", t: t}))
}

// TestValidateDBFile_TruncatedEmptyWAL_RefusedBeforeOpen: a zero-byte WAL cannot repair
// anything, so the truncated database is still refused before any
// connection opens and the empty WAL is left untouched.
func TestValidateDBFile_TruncatedEmptyWAL_RefusedBeforeOpen(t *testing.T) {
	s, dbPath := newDurabilityTestStore(t)
	seedRows(t, s)
	require.NoError(t, s.Close())
	doctorHeaderPageCount(t, dbPath, 1<<20)
	require.NoError(t, os.WriteFile(dbPath+"-wal", nil, 0o600))
	_ = os.Remove(dbPath + "-shm")

	err := validateDBFile(dbPath)
	require.ErrorIs(t, err, model.ErrCorrupted)
	assert.Contains(t, err.Error(), "truncated")

	_, err = New(dbPath, slog.Default())
	require.ErrorIs(t, err, model.ErrCorrupted)
	info, statErr := os.Stat(dbPath + "-wal")
	require.NoError(t, statErr)
	assert.Zero(t, info.Size(), "refusal must not touch the empty WAL")
	_, shmErr := os.Stat(dbPath + "-shm")
	assert.True(t, os.IsNotExist(shmErr), "refusal must not create shared memory")
}
