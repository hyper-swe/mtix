// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
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
