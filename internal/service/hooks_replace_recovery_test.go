// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests recovery across two replaces and the post-reclaim identity boundary.
package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func replaceReviewBoardWithout(t *testing.T, s *sqlite.Store, id string) {
	t.Helper()
	ctx := context.Background()
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	kept := data.Nodes[:0]
	for _, node := range data.Nodes {
		if node.ID != id {
			kept = append(kept, node)
		}
	}
	data.Nodes = kept
	data.NodeCount = len(kept)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	_, err = s.Import(ctx, data, sqlite.ImportModeReplace, len(kept) == 0)
	require.NoError(t, err)
}

func TestHooksReplace_TwoReplacesCannotResurrectMissingClaim(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := sqlite.New(filepath.Join(t.TempDir(), "test.db"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	nodes := NewNodeService(s, nil, nil, logger, time.Now)
	first, err := nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "first", Creator: "first"})
	require.NoError(t, err)
	event, ok, err := s.ReadJournalEventAt(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	won, matched, err := s.ClaimHookDispatchEvent(ctx, "capture", event, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.True(t, matched)
	second, err := nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "second", Creator: "second"})
	require.NoError(t, err)
	replaceReviewBoardWithout(t, s, first.ID)
	_, err = s.WriteDB().Exec(`UPDATE hook_dispatch_ledger SET fired_at='2000-01-01T00:00:00Z'`)
	require.NoError(t, err)
	claims, err := s.StaleHookClaims(ctx, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Empty(t, claims[0].EventID)
	replaceReviewBoardWithout(t, s, second.ID)
	d := NewHooksDispatcher(s, t.TempDir(), logger)
	capture := &encodingCaptureAdapter{name: hooks.AdapterAppendFile}
	d.registry = hooks.NewRegistry(capture)
	d.refireStaleClaim(ctx, hooks.Config{}, claims[0], nil, hooks.ExecDispatchAny)
	var count int
	require.NoError(t, s.ReadDB().QueryRow(`SELECT COUNT(*) FROM hook_dispatch_ledger`).Scan(&count))
	assert.Zero(t, count, "missing snapshot cannot recreate the trimmed row")
	audit, err := s.ReadHookLog(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, audit, "a replaced claim is a quiet no-op")
	_, err = nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "fresh", Creator: "fresh"})
	require.NoError(t, err)
	fresh, ok, err := s.ReadJournalEventAt(ctx, event.Seq)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEqual(t, event.EventID, fresh.EventID)
	require.NoError(t, os.WriteFile(filepath.Join(d.mtixDir, "hooks.yaml"), []byte(`hooks:
  - name: capture
    match: {events: [node.created]}
    deliver: [append-file]
    append-file: {path: capture.jsonl}
`), 0600))
	d.Dispatch(ctx)
	d.Dispatch(ctx)
	require.Len(t, capture.deliveries, 1)
	assert.Equal(t, "fresh", capture.deliveries[0].Event.Author)
}

func TestHooksReplace_RecoveryRereadRejectsChangedIdentity(t *testing.T) {
	var logs bytes.Buffer
	d := newEncodingDispatcher(t, &logs)
	ctx := context.Background()
	nodes := NewNodeService(d.store, nil, nil, d.logger, time.Now)
	_, err := nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "original", Creator: "original"})
	require.NoError(t, err)
	event, ok, err := d.store.ReadJournalEventAt(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	won, matched, err := d.store.ClaimHookDispatchEvent(ctx, "capture", event, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.True(t, matched)
	_, err = d.store.WriteDB().Exec(`UPDATE hook_dispatch_ledger SET fired_at='2000-01-01T00:00:00Z'`)
	require.NoError(t, err)
	claims, err := d.store.StaleHookClaims(ctx, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	// Change identity after the claim guard, before the production recovery read.
	_, err = d.store.WriteDB().Exec(`CREATE TRIGGER replace_after_reclaim AFTER UPDATE OF fired_at ON hook_dispatch_ledger BEGIN UPDATE sync_events SET event_id='replacement-after-reclaim' WHERE rowid=NEW.event_seq; END`)
	require.NoError(t, err)
	capture := &encodingCaptureAdapter{name: hooks.AdapterAppendFile}
	d.registry = hooks.NewRegistry(capture)
	cfg := hooks.Config{Hooks: []hooks.Hook{{Name: "capture", Deliver: []string{hooks.AdapterAppendFile}}}}
	d.refireStaleClaim(ctx, cfg, claims[0], nil, hooks.ExecDispatchAny)
	assert.Empty(t, capture.deliveries)
	assert.Equal(t, uint64(1), d.HookIdentityFenceMisses())
	audit, err := d.store.ReadHookLog(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, audit)
}

func TestHooksReplace_FloorReadBeforeReplaceDispatchesEveryFreshEvent(t *testing.T) {
	var logs bytes.Buffer
	d := newEncodingDispatcher(t, &logs)
	ctx := context.Background()
	nodes := NewNodeService(d.store, nil, nil, d.logger, time.Now)
	_, err := nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "survivor", Creator: "survivor"})
	require.NoError(t, err)
	board, err := d.store.Export(ctx, "", "")
	require.NoError(t, err)
	for range 2 {
		_, err = nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "removed", Creator: "removed"})
		require.NoError(t, err)
	}
	oldTail, err := d.store.JournalTail(ctx)
	require.NoError(t, err)
	require.NoError(t, d.store.AdvanceHookScanFloorClamped(ctx, oldTail))
	require.NoError(t, os.WriteFile(filepath.Join(d.mtixDir, "hooks.yaml"), []byte(`hooks:
  - name: capture
    match: {events: [node.created]}
    deliver: [append-file]
    append-file: {path: capture.jsonl}
`), 0600))
	capture := &encodingCaptureAdapter{name: hooks.AdapterAppendFile}
	d.registry = hooks.NewRegistry(capture)
	d.readJournal = func(ctx context.Context, floor int64, limit int) ([]sqlite.JournalEvent, error) {
		require.Equal(t, oldTail, floor)
		_, err := d.store.Import(ctx, board, sqlite.ImportModeReplace, false)
		require.NoError(t, err)
		for i := range 3 {
			_, err = nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "fresh", Creator: fmt.Sprintf("fresh%d", i)})
			require.NoError(t, err)
		}
		return d.store.ReadJournalSince(ctx, floor, limit)
	}
	d.Dispatch(ctx)
	floor, err := d.store.HookCursor(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), floor)
	assert.Equal(t, uint64(1), d.HookIdentityFenceMisses())
	d.readJournal = d.store.ReadJournalSince
	d.Dispatch(ctx)
	d.Dispatch(ctx)
	authors := make(map[string]int)
	for _, delivery := range capture.deliveries {
		authors[delivery.Event.Author]++
	}
	assert.Equal(t, map[string]int{"fresh0": 1, "fresh1": 1, "fresh2": 1}, authors)
}
