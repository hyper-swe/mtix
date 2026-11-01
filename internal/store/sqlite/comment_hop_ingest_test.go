// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCommentHop_MalformedPulledMetadata_AppliesBodyAndPreservesJournal(t *testing.T) {
	for _, hop := range []string{`null`, `-1`, `"2"`, `1.5`, `9223372036854775808`} {
		t.Run(hop, func(t *testing.T) {
			source, _ := mutationTestStore(t)
			mustCreateNode(t, source, "MTIX-1", "")
			require.NoError(t, source.AppendAnnotation(t.Context(), "MTIX-1", model.Annotation{ID: "remote-comment", Author: "sender", Addressee: "worker", Text: "keep this comment", CreatedAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)}))
			events, err := source.ReadPendingEvents(t.Context(), 100)
			require.NoError(t, err)
			var comment *model.SyncEvent
			for _, event := range events {
				if event.OpType == model.OpComment {
					comment = event
				}
			}
			require.NotNil(t, comment)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(comment.Payload, &fields))
			fields["hop"] = json.RawMessage(hop)
			comment.Payload, err = json.Marshal(fields)
			require.NoError(t, err)
			original := string(comment.Payload)
			target, _ := mutationTestStore(t)
			pullEvents(t, target, events)
			inbox, err := target.InboxList(t.Context(), "worker")
			require.NoError(t, err)
			require.Len(t, inbox, 1)
			require.Equal(t, "keep this comment", inbox[0].Body)
			var stored string
			require.NoError(t, target.ReadDB().QueryRowContext(t.Context(), "SELECT payload FROM sync_events WHERE event_id=?", comment.EventID).Scan(&stored))
			require.Equal(t, original, stored)
			var decoded model.CommentPayload
			require.NoError(t, json.Unmarshal([]byte(stored), &decoded))
			require.Zero(t, decoded.Hop)
		})
	}
}
