// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests claim, outcome and stale-recovery resumptions independently of the
// paused floor-advance regression in hooks_replace_internal_test.go.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type pausedHookReplace struct {
	store          *sqlite.Store
	nodes          *NodeService
	dispatcher     *HooksDispatcher
	capture        *encodingCaptureAdapter
	board          *sqlite.ExportData
	ctx            context.Context
	firstSeq, tail int64
	release        func()
	done           <-chan struct{}
}

func newPausedHookReplace(t *testing.T, count int) pausedHookReplace {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := sqlite.New(filepath.Join(t.TempDir(), "test.db"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	nodes := NewNodeService(s, nil, nil, logger, time.Now)
	_, err = nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "survivor", Creator: "survivor"})
	require.NoError(t, err)
	board, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	d := NewHooksDispatcher(s, t.TempDir(), logger)
	d.Dispatch(ctx)
	floor, err := s.JournalTail(ctx)
	require.NoError(t, err)
	for i := range count {
		_, err = nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: fmt.Sprintf("removed%d", i), Creator: fmt.Sprintf("removed%d", i)})
		require.NoError(t, err)
	}
	tail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(d.mtixDir, "hooks.yaml"), []byte(`hooks:
  - name: replace-capture
    match: {events: [node.created]}
    deliver: [append-file]
    append-file: {path: capture.jsonl}
`), 0600))
	capture := &encodingCaptureAdapter{name: hooks.AdapterAppendFile}
	d.registry = hooks.NewRegistry(capture)
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(resume) }) }
	d.encodeEvent = func(value any) ([]byte, error) {
		first.Do(func() {
			close(entered)
			select {
			case <-resume:
			case <-ctx.Done():
			}
		})
		return json.Marshal(value)
	}
	go func() { defer close(done); d.Dispatch(ctx) }()
	t.Cleanup(func() {
		release()
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("paused pass did not finish")
		}
	})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("pass did not pause")
	}
	return pausedHookReplace{store: s, nodes: nodes, dispatcher: d, capture: capture, board: board, ctx: ctx, firstSeq: floor + 1, tail: tail, release: release, done: done}
}

func (f pausedHookReplace) replaceAndAppend(t *testing.T, count int) {
	t.Helper()
	_, err := f.store.Import(f.ctx, f.board, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	for i := range count {
		_, err = f.nodes.CreateNode(f.ctx, &CreateNodeRequest{Project: "TEST", Title: fmt.Sprintf("fresh%d", i), Creator: fmt.Sprintf("fresh%d", i)})
		require.NoError(t, err)
	}
	tail, err := f.store.JournalTail(f.ctx)
	require.NoError(t, err)
	require.Equal(t, f.tail, tail)
}

func (f pausedHookReplace) resume(t *testing.T) {
	t.Helper()
	f.release()
	select {
	case <-f.done:
	case <-f.ctx.Done():
		t.Fatal("pass did not finish")
	}
	f.dispatcher.encodeEvent = json.Marshal
}

func TestHooksReplace_ClaimAndOutcomeResumeWithOldIdentity(t *testing.T) {
	for _, phase := range []string{"claim", "outcome"} {
		t.Run(phase, func(t *testing.T) {
			count := 1
			if phase == "claim" {
				count = 2
			}
			f := newPausedHookReplace(t, count)
			f.replaceAndAppend(t, count)
			if phase == "outcome" {
				won, err := f.store.ClaimHookDispatch(f.ctx, "replace-capture", f.firstSeq, time.Minute)
				require.NoError(t, err)
				require.True(t, won)
			}
			f.resume(t)
			if phase == "outcome" {
				var outcome string
				require.NoError(t, f.store.ReadDB().QueryRow(`SELECT outcome FROM hook_dispatch_ledger WHERE hook_name='replace-capture' AND event_seq=?`, f.firstSeq).Scan(&outcome))
				require.Equal(t, sqlite.OutcomeClaimed, outcome, "an old completion cannot finalize the new event")
				_, err := f.store.WriteDB().Exec(`UPDATE hook_dispatch_ledger SET fired_at='2000-01-01T00:00:00Z' WHERE hook_name='replace-capture' AND event_seq=?`, f.firstSeq)
				require.NoError(t, err)
			}
			f.dispatcher.Dispatch(f.ctx)
			f.dispatcher.Dispatch(f.ctx)
			authors := make(map[string]int)
			for _, delivery := range f.capture.deliveries {
				authors[delivery.Event.Author]++
			}
			for i := range count {
				assert.Equal(t, 1, authors[fmt.Sprintf("fresh%d", i)])
			}
			assert.Zero(t, authors["survivor"])
			if phase == "claim" {
				assert.Zero(t, authors["removed1"], "second stale snapshot event must not be claimed at its reused sequence")
			}
			assert.GreaterOrEqual(t, f.dispatcher.HookIdentityFenceMisses(), uint64(2))
		})
	}
}

func TestHooksReplace_StaleClaimSnapshotCannotReclaimFreshEvent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	s, err := sqlite.New(filepath.Join(t.TempDir(), "test.db"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	nodes := NewNodeService(s, nil, nil, logger, time.Now)
	board, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	_, err = nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "removed", Creator: "removed"})
	require.NoError(t, err)
	tail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	won, err := s.ClaimHookDispatch(ctx, "stale", tail, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	_, err = s.WriteDB().Exec(`UPDATE hook_dispatch_ledger SET fired_at='2000-01-01T00:00:00Z'`)
	require.NoError(t, err)
	claims, err := s.StaleHookClaims(ctx, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NotEmpty(t, claims[0].EventID)
	_, err = s.Import(ctx, board, sqlite.ImportModeReplace, true)
	require.NoError(t, err)
	_, err = nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "fresh", Creator: "fresh"})
	require.NoError(t, err)
	won, err = s.ClaimHookDispatch(ctx, "stale", tail, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	_, err = s.WriteDB().Exec(`UPDATE hook_dispatch_ledger SET fired_at='2000-01-01T00:00:00Z'`)
	require.NoError(t, err)
	d := NewHooksDispatcher(s, t.TempDir(), logger)
	d.refireStaleClaim(ctx, hooks.Config{}, claims[0], nil, hooks.ExecDispatchAny)
	var outcome, stamp string
	require.NoError(t, s.ReadDB().QueryRow(`SELECT outcome,fired_at FROM hook_dispatch_ledger WHERE hook_name='stale' AND event_seq=?`, tail).Scan(&outcome, &stamp))
	assert.Equal(t, sqlite.OutcomeClaimed, outcome)
	assert.Equal(t, "2000-01-01T00:00:00Z", stamp)
	assert.Equal(t, uint64(1), d.HookIdentityFenceMisses())
}

func TestHooksReplace_InFlightInboxDeliveryCannotAttachToFreshEvent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := sqlite.New(filepath.Join(t.TempDir(), "test.db"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	nodes := NewNodeService(s, nil, nil, logger, time.Now)
	board, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	_, err = nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "removed", Creator: "removed"})
	require.NoError(t, err)
	d := NewHooksDispatcher(s, t.TempDir(), logger)
	require.NoError(t, os.WriteFile(filepath.Join(d.mtixDir, "hooks.yaml"), []byte(`hooks:
  - name: old-only
    match: {events: [node.created], from-agent-not: fresh, to-agent: hook-only}
    deliver: [inbox]
`), 0600))
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	d.encodeEvent = func(value any) ([]byte, error) {
		close(entered)
		select {
		case <-resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return json.Marshal(value)
	}
	go func() { defer close(done); d.Dispatch(ctx) }()
	defer func() {
		release()
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("old inbox delivery did not finish")
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("old inbox delivery did not pause")
	}
	_, err = s.Import(ctx, board, sqlite.ImportModeReplace, true)
	require.NoError(t, err)
	_, err = nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "fresh", Creator: "fresh"})
	require.NoError(t, err)
	release()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("old inbox delivery did not resume")
	}
	d.encodeEvent = json.Marshal
	d.Dispatch(ctx)
	inbox, err := s.InboxList(ctx, "hook-only")
	require.NoError(t, err)
	assert.Empty(t, inbox, "the fresh event does not match the hook and must not inherit the old event's delivery")
	audit, err := s.ReadHookLog(ctx, 10)
	require.NoError(t, err)
	for _, entry := range audit {
		assert.NotEqual(t, sqlite.OutcomeDelivered, entry.Outcome, "a discarded inbox delivery must not be audited as delivered")
	}
	assert.Equal(t, uint64(3), d.HookIdentityFenceMisses(), "inbox, outcome and floor misses are counted")
}
