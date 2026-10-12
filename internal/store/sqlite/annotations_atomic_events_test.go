// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func commentPayloads(t *testing.T, events []eventRow) []model.CommentPayload {
	t.Helper()
	out := make([]model.CommentPayload, 0, len(events))
	for _, ev := range events {
		var p model.CommentPayload
		require.NoError(t, json.Unmarshal([]byte(ev.Payload), &p))
		out = append(out, p)
	}
	return out
}

// TestAppendAnnotation_EmitsOneCommentEventPerAppend_WithNewestBody verifies
// each append journals exactly one comment event whose body, author and
// addressee are the NEW annotation's, also after earlier annotations exist
// (the previous array feeds the new-entry detection; MTIX-148).
func TestAppendAnnotation_EmitsOneCommentEventPerAppend_WithNewestBody(t *testing.T) {
	s, raw := mutationTestStore(t)
	ctx := context.Background()
	mustCreateNode(t, s, "MTIX-1", "")
	now := time.Now().UTC()

	require.NoError(t, s.AppendAnnotation(ctx, "MTIX-1", model.Annotation{ID: "a1", Author: "alice", Text: "first", CreatedAt: now}))
	require.NoError(t, s.AppendAnnotation(ctx, "MTIX-1", model.Annotation{ID: "a2", Author: "bob", Text: "second", CreatedAt: now, Addressee: "carol"}))
	require.NoError(t, s.AppendAnnotation(ctx, "MTIX-1", model.Annotation{ID: "a3", Author: "dave", Text: "third", CreatedAt: now}))

	events := readEventsByOp(t, raw, model.OpComment)
	require.Len(t, events, 3, "exactly one comment event per append")
	payloads := commentPayloads(t, events)
	want := []struct{ author, body, to string }{
		{"alice", "first", ""}, {"bob", "second", "carol"}, {"dave", "third", ""},
	}
	for i, w := range want {
		require.Equal(t, w.author, events[i].AuthorID, "event author %d", i)
		require.Equal(t, w.author, payloads[i].AuthorID, "payload author %d", i)
		require.Equal(t, w.body, payloads[i].Body, "payload body %d", i)
		require.Equal(t, w.to, payloads[i].To, "payload to %d", i)
	}
}

// TestResolveAnnotation_EmitsExactlyOneEvent_AndFailedResolveNone verifies a
// resolve journals one event, and an unknown annotation or node journals none.
func TestResolveAnnotation_EmitsExactlyOneEvent_AndFailedResolveNone(t *testing.T) {
	s, raw := mutationTestStore(t)
	ctx := context.Background()
	mustCreateNode(t, s, "MTIX-1", "")
	now := time.Now().UTC()
	require.NoError(t, s.AppendAnnotation(ctx, "MTIX-1", model.Annotation{ID: "a1", Author: "alice", Text: "x", CreatedAt: now}))
	require.NoError(t, s.AppendAnnotation(ctx, "MTIX-1", model.Annotation{ID: "a2", Author: "bob", Text: "y", CreatedAt: now}))
	before := countSyncEvents(t, raw)

	require.NoError(t, s.ResolveAnnotation(ctx, "MTIX-1", "a1", true))
	require.Equal(t, before+1, countSyncEvents(t, raw), "one event per resolve")

	require.ErrorIs(t, s.ResolveAnnotation(ctx, "MTIX-1", "missing", true), model.ErrNotFound)
	require.ErrorIs(t, s.ResolveAnnotation(ctx, "GHOST-1", "a1", true), model.ErrNotFound)
	require.Equal(t, before+1, countSyncEvents(t, raw), "failed resolves write no event")
}

// TestMutateAnnotations_DeletedNodeAndWriteFailure_ReturnErrorsWithoutEvents
// covers the refusal paths: a soft-deleted node reports ErrNotFound and
// leaves no event; a failing UPDATE returns its error, rolls the event back
// and leaves the stored annotations unchanged.
func TestMutateAnnotations_DeletedNodeAndWriteFailure_ReturnErrorsWithoutEvents(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	ann := model.Annotation{ID: "a1", Author: "alice", Text: "x", CreatedAt: now}

	t.Run("soft-deleted node", func(t *testing.T) {
		s, raw := mutationTestStore(t)
		mustCreateNode(t, s, "MTIX-1", "")
		require.NoError(t, s.AppendAnnotation(ctx, "MTIX-1", ann))
		require.NoError(t, s.DeleteNode(ctx, "MTIX-1", false, "alice"))
		before := countSyncEvents(t, raw)

		require.ErrorIs(t, s.AppendAnnotation(ctx, "MTIX-1", model.Annotation{ID: "a2"}), model.ErrNotFound)
		require.ErrorIs(t, s.ResolveAnnotation(ctx, "MTIX-1", "a1", true), model.ErrNotFound)
		require.Equal(t, before, countSyncEvents(t, raw), "no event for a deleted node")
	})

	t.Run("failing update", func(t *testing.T) {
		s, raw := mutationTestStore(t)
		mustCreateNode(t, s, "MTIX-1", "")
		require.NoError(t, s.AppendAnnotation(ctx, "MTIX-1", ann))
		before := countSyncEvents(t, raw)
		_, err := s.WriteDB().Exec(`CREATE TRIGGER block_annotations BEFORE UPDATE OF annotations ON nodes
			BEGIN SELECT RAISE(ABORT, 'annotations locked'); END`)
		require.NoError(t, err)

		require.ErrorContains(t, s.AppendAnnotation(ctx, "MTIX-1", model.Annotation{ID: "a2"}), "annotations locked")
		require.ErrorContains(t, s.ResolveAnnotation(ctx, "MTIX-1", "a1", true), "annotations locked")
		require.Equal(t, before, countSyncEvents(t, raw), "a failed write leaves no event")
		got, err := s.GetNode(ctx, "MTIX-1")
		require.NoError(t, err)
		require.Len(t, got.Annotations, 1)
		require.False(t, got.Annotations[0].Resolved)
	})

	t.Run("unencodable annotation", func(t *testing.T) {
		s, raw := mutationTestStore(t)
		mustCreateNode(t, s, "MTIX-1", "")
		before := countSyncEvents(t, raw)
		// A timestamp outside years 0..9999 cannot be encoded as JSON.
		bad := model.Annotation{ID: "bad", Author: "alice", Text: "x", CreatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}

		require.ErrorContains(t, s.AppendAnnotation(ctx, "MTIX-1", bad), "marshal annotations")
		require.Equal(t, before, countSyncEvents(t, raw))
		got, err := s.GetNode(ctx, "MTIX-1")
		require.NoError(t, err)
		require.Empty(t, got.Annotations)
	})
}
