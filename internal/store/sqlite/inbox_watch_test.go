// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-326: the token is stable while nothing commits, moves on every commit
// (including one from a second connection), and is read without a query.
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

// MTIX-326 review S2: after a checkpoint the WAL restarts in place, so a
// commit can leave the size unchanged, and a coarse filesystem clock can leave
// the mtime unchanged too. The WAL index commit counter must still move the
// token.
func TestInboxChangeToken_CommitKeepsSizeAndMtime_StillMoves(t *testing.T) {
	s := inboxWaitStore(t)
	_, err := s.writeDB.ExecContext(t.Context(), `PRAGMA wal_checkpoint(PASSIVE)`)
	require.NoError(t, err)
	wal, db := s.dbPath+"-wal", s.dbPath
	walInfo, err := os.Stat(wal)
	require.NoError(t, err)
	dbInfo, err := os.Stat(db)
	require.NoError(t, err)
	before, err := s.InboxChangeToken(t.Context())
	require.NoError(t, err)

	require.NoError(t, s.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{{
		ID: "cx", Author: "sender", Text: "x", Addressee: "w", CreatedAt: inboxWaitTime(),
	}}))
	// Restore the timestamps: the stat part of the token cannot tell the commits apart.
	require.NoError(t, os.Chtimes(wal, walInfo.ModTime(), walInfo.ModTime()))
	require.NoError(t, os.Chtimes(db, dbInfo.ModTime(), dbInfo.ModTime()))
	walAfter, err := os.Stat(wal)
	require.NoError(t, err)
	require.Equal(t, walInfo.Size(), walAfter.Size(), "precondition: the WAL restarted in place at the same size")

	after, err := s.InboxChangeToken(t.Context())
	require.NoError(t, err)
	assert.NotEqual(t, before, after, "a commit must move the token although size and mtime did not")
}

// Without a usable -shm (rollback-journal mode, never opened in WAL, or a
// short file) the main database header's change counter keeps the token
// moving. The -shm is never created or written by the token.
func TestInboxChangeToken_NoShm_FallsBackToDatabaseChangeCounter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.db")
	when := inboxWaitTime()
	write := func(counter byte) {
		hdr := make([]byte, 100)
		hdr[27] = counter
		require.NoError(t, os.WriteFile(path, hdr, 0o600))
		require.NoError(t, os.Chtimes(path, when, when))
	}
	s := &Store{dbPath: path, logger: slog.Default()}
	write(1)
	a, err := s.InboxChangeToken(t.Context())
	require.NoError(t, err)
	write(2)
	b, err := s.InboxChangeToken(t.Context())
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "same size and mtime, different change counter")

	require.NoError(t, os.WriteFile(path+"-shm", []byte("short"), 0o600))
	c, err := s.InboxChangeToken(t.Context())
	require.NoError(t, err, "a short -shm is a fallback, not an error")
	assert.Equal(t, b, c)

	require.NoError(t, os.Remove(path+"-shm"))
	_, err = s.InboxChangeToken(t.Context())
	require.NoError(t, err)
	_, statErr := os.Stat(path + "-shm")
	assert.True(t, os.IsNotExist(statErr), "the token must never create the -shm")
}

// An -shm that cannot be read (here a directory stands in for a platform that
// refuses to open it) falls back and is logged once, not on every tick.
func TestInboxChangeToken_ShmUnreadable_FallsBackAndLogsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.db")
	require.NoError(t, os.WriteFile(path, make([]byte, 100), 0o600))
	require.NoError(t, os.Mkdir(path+"-shm", 0o700))
	var logs bytes.Buffer
	s := &Store{dbPath: path, logger: slog.New(slog.NewTextHandler(&logs, nil))}
	for i := 0; i < 3; i++ {
		_, err := s.InboxChangeToken(t.Context())
		require.NoError(t, err)
	}
	assert.Equal(t, 1, strings.Count(logs.String(), "WAL index unreadable"), "logged once")
}
