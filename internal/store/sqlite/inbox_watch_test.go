// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-326: the token is stable while nothing commits and moves on every
// commit, and is read without a query.
func TestInboxChangeToken_StableWhenIdle_MovesOnCommit(t *testing.T) {
	s := inboxWaitStore(t)
	var _ InboxChangeStore = s
	a, err := s.InboxChangeToken(t.Context())
	require.NoError(t, err)
	b, err := s.InboxChangeToken(t.Context())
	require.NoError(t, err)
	assert.Equal(t, a, b, "no commit, no change")
	for i := 0; i < 3; i++ {
		before, err := s.InboxChangeToken(t.Context())
		require.NoError(t, err)
		require.NoError(t, s.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{{
			ID: "c" + string(rune('a'+i)), Author: "sender", Text: "x", Addressee: "w", CreatedAt: inboxWaitTime(),
		}}))
		after, err := s.InboxChangeToken(t.Context())
		require.NoError(t, err)
		assert.NotEqual(t, before, after, "commit %d must move the token", i)
	}
}

func TestInboxChangeToken_CancelledContext_ReturnsError(t *testing.T) {
	s := inboxWaitStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.InboxChangeToken(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

// MTIX-326: in the normal case a commit changes the size or the nanosecond
// mtime of the WAL (or the database), so the stat token moves after a
// checkpoint too. The rare commit that changes neither is the documented gap
// closed by the 60 second safety read.
func TestInboxChangeToken_CommitAfterCheckpoint_MovesStatToken(t *testing.T) {
	s := inboxWaitStore(t)
	_, err := s.writeDB.ExecContext(t.Context(), `PRAGMA wal_checkpoint(PASSIVE)`)
	require.NoError(t, err)
	statPair := func() (int64, int64, int64, int64) {
		w, werr := os.Stat(s.dbPath + "-wal")
		require.NoError(t, werr)
		d, derr := os.Stat(s.dbPath)
		require.NoError(t, derr)
		return w.Size(), w.ModTime().UnixNano(), d.Size(), d.ModTime().UnixNano()
	}
	before, err := s.InboxChangeToken(t.Context())
	require.NoError(t, err)
	ws, wm, ds, dm := statPair()

	require.NoError(t, s.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{{
		ID: "cx", Author: "sender", Text: "x", Addressee: "w", CreatedAt: inboxWaitTime(),
	}}))

	ws2, wm2, ds2, dm2 := statPair()
	require.True(t, ws != ws2 || wm != wm2 || ds != ds2 || dm != dm2,
		"precondition: the commit changed a size or a nanosecond mtime")
	after, err := s.InboxChangeToken(t.Context())
	require.NoError(t, err)
	assert.NotEqual(t, before, after, "the stat token must move on a commit")
}

// MTIX-326 design rule (review round 2 S1): the token makes no file-system
// call except stat on the database, -wal and -shm paths. It never opens them,
// because closing any descriptor on a SQLite file in the process that holds
// the connections releases SQLite's POSIX locks.
func TestInboxChangeToken_OnlyStatsTheThreeDatabaseFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	var stated []string
	fi, err := os.Stat(t.TempDir())
	require.NoError(t, err)
	stat := func(name string) (fs.FileInfo, error) {
		stated = append(stated, name)
		if name == path+"-shm" {
			return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
		}
		return fi, nil
	}
	tok, err := inboxChangeTokenFor(t.Context(), path, stat)
	require.NoError(t, err)
	assert.Equal(t, []string{path, path + "-wal", path + "-shm"}, stated)
	assert.Contains(t, tok, "-|", "a missing file is part of the token, not an error")
}

// The same rule by code structure: the token source contains no call that
// opens or reads a file.
func TestInboxChangeToken_SourceNeverOpensFiles(t *testing.T) {
	src, err := os.ReadFile("inbox_watch.go")
	require.NoError(t, err)
	for _, banned := range []string{"os.Open", "os.OpenFile", "os.ReadFile", "os.Create", "syscall.", "mmap", "ReadAt"} {
		assert.NotContains(t, string(src), banned, "the token must stay stat-only (MTIX-326)")
	}
}

func TestInboxChangeToken_StatFailure_ReturnsWrappedError(t *testing.T) {
	boom := errors.New("permission denied")
	_, err := inboxChangeTokenFor(t.Context(), "/x/board.db", func(string) (fs.FileInfo, error) { return nil, boom })
	require.ErrorIs(t, err, boom)
}
