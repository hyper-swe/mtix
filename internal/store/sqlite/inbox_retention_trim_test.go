// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.2 round 3 (F10): the replace trim lowers only the cursors that
// point past the surviving journal tail. A cursor below the tail must stay,
// or the unread mail between it and the tail would be acknowledged silently.
func TestInboxRetention_ReplaceDroppingNewestEvents_ClampsOnlyCursorsAboveTail(t *testing.T) {
	ctx := context.Background()
	s, raw := retentionStore(t)
	addressTo(t, s, "bea", "PROJ-1")
	addressTo(t, s, "bea", "PROJ-2")
	addressTo(t, s, "ann", "PROJ-3")
	bea, err := s.InboxList(ctx, "bea")
	require.NoError(t, err)
	require.Len(t, bea, 2)
	ann, err := s.InboxList(ctx, "ann")
	require.NoError(t, err)
	require.Len(t, ann, 1)
	require.NoError(t, s.InboxAckThrough(ctx, "bea", bea[0].Seq))
	require.NoError(t, s.InboxAckThrough(ctx, "ann", ann[0].Seq))
	tail := bea[1].Seq
	require.Greater(t, ann[0].Seq, tail, "premise: ann's cursor is above the surviving tail")

	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	data = withoutExportNode(data, "PROJ-3")
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	_, err = s.Import(ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	cursor := func(agent string) int64 {
		var c int64
		require.NoError(t, raw.QueryRow(`SELECT cursor FROM agent_inbox_cursor WHERE agent_id = ?`, agent).Scan(&c))
		return c
	}
	assert.Equal(t, tail, cursor("ann"), "a cursor above the tail is lowered to it")
	assert.Equal(t, bea[0].Seq, cursor("bea"), "a cursor below the tail is left alone")

	got, err := s.InboxList(ctx, "bea")
	require.NoError(t, err)
	require.Len(t, got, 1, "bea's unread mail below the tail is still unread")
	assert.Equal(t, "PROJ-2", got[0].NodeID)
	hist, err := s.InboxHistory(ctx, "bea")
	require.NoError(t, err)
	require.Len(t, hist, 2)
	assert.Equal(t, sqlite.InboxUnacked, hist[1].State)
}

// MTIX-112.2 round 3 (F11): a database path holding characters that end a
// file: URI path early still opens read-only at the real path.
func TestCountUnackedInboxFile_PathWithURISpecials_CountsTheRealFile(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "mtix.db")
	s, err := sqlite.New(plain, nil)
	require.NoError(t, err)
	addressTo(t, s, "opus", "PROJ-1")
	require.NoError(t, s.Close())
	// The store itself does not open such a path, so move the closed file.
	dir := filepath.Join(t.TempDir(), "a?b%25c#d")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	path := filepath.Join(dir, "mtix.db")
	require.NoError(t, os.Rename(plain, path))

	n, err := sqlite.CountUnackedInboxFile(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}
