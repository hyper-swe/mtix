// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestInbox_AddressedComment_ListAck: an addressed comment lands in exactly the
// addressee's inbox and clears on ack (MTIX-47.1 / FR-19.4).
func TestInbox_AddressedComment_ListAck(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Task", now)))

	require.NoError(t, s.SetAnnotations(ctx, "PROJ-1", []model.Annotation{
		{ID: "01ANN", Author: "worker", Text: "ruling: proceed", CreatedAt: now, Addressee: "opus-4-8"},
	}))

	got, err := s.InboxList(ctx, "opus-4-8")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "ruling: proceed", got[0].Body)
	require.Equal(t, "PROJ-1", got[0].NodeID)
	require.Positive(t, got[0].Seq)

	// A different agent sees nothing (addressee filter).
	other, err := s.InboxList(ctx, "someone-else")
	require.NoError(t, err)
	require.Empty(t, other)

	// Ack clears it; a lower/equal seq is idempotent.
	require.NoError(t, s.InboxAck(ctx, "opus-4-8", got[0].Seq))
	after, err := s.InboxList(ctx, "opus-4-8")
	require.NoError(t, err)
	require.Empty(t, after)
	require.NoError(t, s.InboxAck(ctx, "opus-4-8", 0)) // must not rewind
	stillEmpty, err := s.InboxList(ctx, "opus-4-8")
	require.NoError(t, err)
	require.Empty(t, stillEmpty)
}

// TestInbox_Wait_WakesOnAddressedComment: a parked waiter wakes when a matching
// comment is journaled — the primitive a worker loop parks on.
func TestInbox_Wait_WakesOnAddressedComment(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Task", now)))

	done := make(chan []sqlite.InboxEvent, 1)
	go func() {
		ev, _ := s.InboxWait(ctx, "opus-4-8", 5*time.Second)
		done <- ev
	}()
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, s.SetAnnotations(ctx, "PROJ-1", []model.Annotation{
		{ID: "01ANN", Author: "worker", Text: "wake up", CreatedAt: now, Addressee: "opus-4-8"},
	}))

	select {
	case ev := <-done:
		require.Len(t, ev, 1)
		require.Equal(t, "wake up", ev[0].Body)
	case <-time.After(6 * time.Second):
		t.Fatal("InboxWait did not wake on the addressed comment")
	}
}

// TestInbox_Wait_TimesOutEmpty: no matching event -> (nil, nil) after timeout.
func TestInbox_Wait_TimesOutEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	start := time.Now()
	ev, err := s.InboxWait(ctx, "nobody", 300*time.Millisecond)
	require.NoError(t, err)
	require.Empty(t, ev)
	require.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond)
}
