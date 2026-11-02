// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The real released binary must tolerate the additive reply_by comment key.
package sqlite_test

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestReplyBy_054BinarySyncsBoard(t *testing.T) {
	binary := compat054Binary(t)
	f := newWakeCompatFixture(t)
	pending, err := f.store.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	var nodeID string
	for _, e := range pending {
		nodeID = e.NodeID
	}
	require.NotEmpty(t, nodeID)
	clock := func() time.Time { return time.Date(2026, 10, 15, 12, 5, 0, 0, time.UTC) }
	prompts := service.NewPromptService(f.store, &service.NoopBroadcaster{}, slog.Default(), clock)
	res, err := prompts.AddAnnotationRequest(t.Context(), service.AnnotationRequest{
		NodeID: nodeID, Text: "answer within the hour", Author: "test-author", Addressee: "test-agent", AwaitReply: time.Hour})
	require.NoError(t, err)

	exported, err := f.store.Export(t.Context(), "TEST", "reply-by-test")
	require.NoError(t, err)
	raw, err := json.MarshalIndent(exported, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.project, ".mtix", "tasks.json"), raw, 0o600))

	events, err := f.store.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	var carrying *model.SyncEvent
	for _, e := range events {
		var p struct {
			ReplyBy string `json:"reply_by"`
		}
		if e.OpType == model.OpComment && json.Unmarshal(e.Payload, &p) == nil && p.ReplyBy != "" {
			carrying = e
			require.Equal(t, model.FormatReplyBy(res.ReplyBy), p.ReplyBy)
		}
	}
	require.NotNil(t, carrying, "the addressed event carries reply_by on the wire")

	// The released binary opens the store, reads the inbox and syncs without
	// choking on the additive key, and leaves the journal row untouched.
	out := attachReaderCommand(t, binary, f.project, "", "inbox", "--agent", "test-agent", "--json")
	require.Contains(t, string(out), "answer within the hour")
	attachReaderCommand(t, binary, f.project, "", "sync", "--json")
	var payload string
	require.NoError(t, f.store.ReadDB().QueryRowContext(t.Context(),
		"SELECT payload FROM sync_events WHERE event_id=?", carrying.EventID).Scan(&payload))
	require.JSONEq(t, string(carrying.Payload), payload)

	// A second replica applying the event keeps it addressed and readable.
	target, _ := mutationTestStore(t)
	pullEvents(t, target, events)
	inbox, err := target.InboxList(t.Context(), "test-agent")
	require.NoError(t, err)
	require.NotEmpty(t, inbox)
}
