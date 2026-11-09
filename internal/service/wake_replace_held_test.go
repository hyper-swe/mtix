// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func replaceHeldWake(t *testing.T, f wakeReplaceFixture, lower bool) []int64 {
	t.Helper()
	if lower {
		return f.replaceAndAppend(t, 2)
	}
	_, err := f.store.Import(t.Context(), f.board, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	nodes := service.NewNodeService(f.store, &service.NoopBroadcaster{}, &service.StaticConfig{}, slog.Default(), time.Now)
	node, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "PROJ", Title: "fresh", Creator: "sender"})
	require.NoError(t, err)
	require.NoError(t, f.prompts.AddAnnotation(t.Context(), node.ID, "fresh", "sender", f.key.AgentID))
	events, err := f.store.InboxList(t.Context(), f.key.AgentID)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, f.droppedTail, events[1].Seq)
	return []int64{events[1].Seq}
}

func TestWakeReevaluate_ReplaceAfterHeldSnapshot_FreshMailRemainsEligible(t *testing.T) {
	for _, lower := range []bool{false, true} {
		t.Run(map[bool]string{false: "same addressed seq", true: "lower addressed event"}[lower], func(t *testing.T) {
			f := newWakeReplaceFixture(t, 1, false)
			require.NoError(t, f.store.SetWakeHold(t.Context(), f.key, 0, true))
			var fresh []int64
			f.gate = func(context.Context, model.WakeKey) (bool, error) {
				fresh = replaceHeldWake(t, f, lower)
				return true, nil
			}
			err := f.d.Reevaluate(t.Context(), f.key.AgentID)
			if lower {
				require.ErrorIs(t, err, model.ErrWakeHeldEventMissing, "a newly arrived lower event is absent from the old pass")
			} else {
				require.NoError(t, err)
			}
			require.NotEmpty(t, fresh, "replacement ran after the real held-candidate query")
			assert.Equal(t, f.oldSends, f.adapter.seqs(), "a stale held snapshot cannot claim a replacement event")
			mark, held := f.mark()
			assert.Equal(t, f.survivorTail, mark)
			assert.True(t, held, "the old pass must not clear the pending hold")
			f.gate = func(context.Context, model.WakeKey) (bool, error) { return true, nil }
			require.NoError(t, f.scanner(always).Scan(t.Context()))
			require.NoError(t, f.scanner(always).Scan(t.Context()))
			assert.Equal(t, append(append([]int64(nil), f.oldSends...), fresh...), f.adapter.seqs())
		})
	}
}
