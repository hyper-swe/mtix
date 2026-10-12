// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Concrete owned executables exercise produced-output limits and finite process budgets.
package service

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMergeBaseBoard_OutputOverLimit_ErrNoMergeBase(t *testing.T) {
	root := boardSingleRepo(t, boardBytes(t, "ancestor board"))
	raw := boardBytes(t, "ancestor board")
	for _, limit := range []int64{int64(len(raw)), int64(len(raw)) - 1} {
		svc := NewSyncService(nil, nil, nil)
		svc.MaxImportSize = limit
		data, _, err := svc.MergeBaseBoard(context.Background(), root)
		if limit == int64(len(raw)) {
			require.NoError(t, err)
			require.Equal(t, "ancestor board", data.Nodes[0].Title)
		} else {
			require.ErrorIs(t, err, ErrNoMergeBase)
			require.Nil(t, data)
		}
	}
	for _, body := range []string{"printf '123456789'", "printf '123456789' >&2", "printf '%s' 'PRIVATE_STDERR_MARKER' >&2; exit 1"} {
		script := boardScript(t, body)
		capBytes := int64(8)
		if strings.Contains(body, "123456789' >&2") {
			body = strings.Repeat("x", boardStderrLimit+1)
			script = boardScript(t, "printf '%s' '"+body+"' >&2")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := readGit(ctx, script, root, capBytes, "show")
		cancel()
		require.ErrorIs(t, err, ErrNoMergeBase)
		require.NotContains(t, err.Error(), "PRIVATE_STDERR_MARKER")
	}
}

func TestMergeBaseBoard_Timeout_ErrNoMergeBase(t *testing.T) {
	script := boardScript(t, "while :; do :; done")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := readGit(ctx, boardTrustedScript(t, script), t.TempDir(), 1024, "show")
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 5*time.Second, gitBoardTimeout)
}

func TestMergeBaseBoard_OIDValidation_IsBoundedAndPrivate(t *testing.T) {
	for _, raw := range []string{"", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("z", 40), strings.Repeat("g", 64), strings.Repeat("a", 40) + "\n" + strings.Repeat("b", 40), "PRIVATE_OID_MARKER" + strings.Repeat("a", 40)} {
		_, err := boardOID([]byte(raw))
		require.ErrorIs(t, err, ErrNoMergeBase)
		require.Less(t, len(err.Error()), 200)
		require.NotContains(t, err.Error(), "PRIVATE_OID_MARKER")
	}
	for _, length := range []int{40, 64} {
		oid, err := boardOID([]byte(strings.Repeat("A", length) + "\n"))
		require.NoError(t, err)
		require.Len(t, oid, length)
	}
}

func TestMergeBaseBoard_NoGitBinary_ErrNoMergeBase(t *testing.T) {
	root := boardSingleRepo(t, boardBytes(t, "ancestor board"))
	before := boardSnapshot(t, root)
	data, _, err := readMergeBaseBoard(context.Background(), root, filepath.Join(t.TempDir(), "absent-git"), DefaultMaxImportSize)
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.Nil(t, data)
	require.Equal(t, before, boardSnapshot(t, root))
}

func TestMergeBaseBoard_OutputChunks_StayBoundedAtBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &boardOutput{limit: 8, cancel: cancel}
	n, err := out.Write([]byte("12345678"))
	require.NoError(t, err)
	require.Equal(t, 8, n)
	_, err = out.Write([]byte("9"))
	require.Error(t, err)
	data, overflow := out.result()
	require.True(t, overflow)
	require.Equal(t, "12345678", string(data))
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	_, err = readGit(context.Background(), "/absent", t.TempDir(), 0, "show")
	require.ErrorIs(t, err, ErrNoMergeBase)
}
