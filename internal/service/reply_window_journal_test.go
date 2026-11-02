// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type journalFixture struct {
	store   *sqlite.Store
	prompts *service.PromptService
	node    string
	now     time.Time
}

func newJournalFixture(t *testing.T) *journalFixture {
	t.Helper()
	s, err := sqlite.New(filepath.Join(t.TempDir(), "mtix.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	f := &journalFixture{store: s, now: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return f.now }
	s.SetClock(clock)
	nodes := service.NewNodeService(s, &service.NoopBroadcaster{}, &service.StaticConfig{}, slog.Default(), clock)
	n, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "reply", Creator: "me"})
	require.NoError(t, err)
	f.node = n.ID
	f.prompts = service.NewPromptService(s, &service.NoopBroadcaster{}, slog.Default(), clock)
	return f
}

func (f *journalFixture) send(t *testing.T, author, to string, wait time.Duration) service.AnnotationResult {
	t.Helper()
	f.now = f.now.Add(time.Second)
	res, err := f.prompts.AddAnnotationRequest(t.Context(), service.AnnotationRequest{
		NodeID: f.node, Text: "question", Author: author, Addressee: to, AwaitReply: wait})
	require.NoError(t, err)
	return res
}

func TestAddAnnotationRequest_AwaitReply_CarriesReplyByInPayload(t *testing.T) {
	f := newJournalFixture(t)
	res := f.send(t, "me", "peer", 30*time.Minute)
	require.Equal(t, f.now.Add(30*time.Minute), res.ReplyBy)
	evs, err := f.store.ReplyByEvents(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, evs, 1)
	require.Equal(t, "me", evs[0].Author)
	require.Equal(t, "peer", evs[0].To)
	require.Equal(t, model.FormatReplyBy(res.ReplyBy), evs[0].ReplyBy)
}

func TestAddAnnotationRequest_NoAwait_NoReplyByEvent(t *testing.T) {
	f := newJournalFixture(t)
	f.send(t, "me", "peer", 0)
	f.send(t, "me", "", 0)
	evs, err := f.store.ReplyByEvents(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Empty(t, evs, "an ordinary or plain addressed comment carries no deadline")
}

func TestAddAnnotationRequest_Table_ClampAndRejects(t *testing.T) {
	f := newJournalFixture(t)
	res := f.send(t, "me", "peer", 100*time.Hour)
	require.Equal(t, f.now.Add(8*time.Hour), res.ReplyBy, "clamped to eight hours")
	for name, req := range map[string]service.AnnotationRequest{
		"await without an addressee": {NodeID: f.node, Text: "x", Author: "me", AwaitReply: time.Minute},
		"negative await":             {NodeID: f.node, Text: "x", Author: "me", Addressee: "peer", AwaitReply: -time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.prompts.AddAnnotationRequest(t.Context(), req)
			require.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

func TestReplyJournal_ReplyArrived_DirectionAndWatermark(t *testing.T) {
	f := newJournalFixture(t)
	f.send(t, "me", "peer", time.Hour)
	mark, err := f.store.LatestSyncSeq(t.Context())
	require.NoError(t, err)
	got, err := f.store.ReplyArrived(t.Context(), "peer", "me", mark)
	require.NoError(t, err)
	require.False(t, got, "nothing from peer yet")
	f.send(t, "peer", "me", 0)
	for name, tt := range map[string]struct {
		from, to string
		after    int64
		want     bool
	}{
		"the awaited reply":          {"peer", "me", mark, true},
		"wrong direction":            {"me", "peer", mark, true}, // my own question is after seq 0 only
		"reply before the watermark": {"peer", "me", mark + 5, false},
		"someone else's reply":       {"stranger", "me", mark, false},
		"reply addressed to a third": {"peer", "other", mark, false},
	} {
		t.Run(name, func(t *testing.T) {
			after := tt.after
			if name == "wrong direction" {
				after = 0
			}
			ok, err := f.store.ReplyArrived(t.Context(), tt.from, tt.to, after)
			require.NoError(t, err)
			require.Equal(t, tt.want, ok)
		})
	}
	_, err = f.store.ReplyArrived(t.Context(), "", "me", 0)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	_, err = f.store.ReplyByEvents(t.Context(), 0, 0)
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

func TestReplyProcessor_RealJournal_HoldsOnlyForRegisteredAddressee(t *testing.T) {
	f := newJournalFixture(t)
	fx := newReplyFixture(t, f.now, f.now)
	fx.proc.Journal = f.store // real journal; no agent is registered on this host
	f.send(t, "stranger", "local-agent", 4*time.Hour)
	require.NoError(t, fx.proc.Scan(t.Context()))
	require.Empty(t, fx.replyState().Holds, "To is not registered on this host")
	require.Positive(t, fx.replyState().Cursor, "the event was examined and will not be re-read")
}
