// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestWakeEventHop_StoredContextAndMalformedPayload(t *testing.T) {
	s, _ := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	require.NoError(t, s.AppendAnnotation(t.Context(), "MTIX-1", model.Annotation{ID: "hop-event", Author: "sender", Text: "addressed", Addressee: "worker", Hop: 33, CreatedAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)}))
	inbox, err := s.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, inbox, 1)
	key := model.WakeKey{AgentID: "worker", EventSeq: inbox[0].Seq}
	hop, err := s.WakeEventHop(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, int64(33), hop)
	missing := key
	missing.EventSeq = 99999
	hop, err = s.WakeEventHop(t.Context(), missing)
	require.NoError(t, err)
	require.Zero(t, hop)
	for _, tt := range []struct {
		raw string
		bad bool
	}{
		{`{"body":"unaddressed","hop":33}`, false}, {`{"to":"worker"}`, false},
		{`{"to":"worker","hop":-1}`, true}, {`{"to":"worker","hop":null}`, true}, {`{"to":"worker","hop":1.5}`, true},
	} {
		t.Run(tt.raw, func(t *testing.T) {
			_, err := s.WriteDB().ExecContext(t.Context(), "UPDATE sync_events SET payload=? WHERE rowid=?", tt.raw, key.EventSeq)
			require.NoError(t, err)
			hop, err := s.WakeEventHop(t.Context(), key)
			if tt.bad {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Zero(t, hop)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = s.WakeEventHop(ctx, key)
	require.ErrorIs(t, err, context.Canceled)
}
