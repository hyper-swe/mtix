// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests a dispatcher holding an old journal snapshot across replace.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestHooksReplace_ActivePassDoesNotConsumeReusedSequence(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := sqlite.New(filepath.Join(t.TempDir(), "test.db"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	svc := NewNodeService(s, nil, nil, logger, time.Now)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = svc.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "survivor", Creator: "survivor"})
	require.NoError(t, err)
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	d := NewHooksDispatcher(s, t.TempDir(), logger)
	d.Dispatch(ctx) // surviving history is already consumed
	_, err = svc.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "removed", Creator: "removed"})
	require.NoError(t, err)
	oldTail, err := s.JournalTail(ctx)
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
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	defer release()
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
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("dispatcher did not reach delivery")
	}
	_, err = s.Import(ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	_, err = svc.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "fresh", Creator: "fresh"})
	require.NoError(t, err)
	freshTail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.Equal(t, oldTail, freshTail, "premise: new event reuses the in-flight event sequence")
	release()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("dispatcher did not finish; possible lock-order deadlock")
	}
	d.encodeEvent = json.Marshal
	d.Dispatch(ctx)
	d.Dispatch(ctx)
	var fresh, survivor int
	for _, delivery := range capture.deliveries {
		if delivery.Event.Author == "fresh" {
			fresh++
		}
		if delivery.Event.Author == "survivor" {
			survivor++
		}
	}
	assert.Equal(t, 1, fresh, "the old pass must not advance the cursor or finalize the fresh event's claim")
	assert.Zero(t, survivor, "consumed surviving history must not run twice")
	assert.GreaterOrEqual(t, d.HookIdentityFenceMisses(), uint64(2), "outcome and floor misses are counted")
}

func TestHooksReplace_NewEventDispatchesOnce(t *testing.T) {
	for _, empty := range []bool{false, true} {
		for _, state := range []string{"cursor", "delivered", "claimed"} {
			t.Run(fmt.Sprintf("empty=%t/%s", empty, state), func(t *testing.T) {
				ctx := context.Background()
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				s, err := sqlite.New(filepath.Join(t.TempDir(), "test.db"), logger)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, s.Close()) })
				svc := NewNodeService(s, nil, nil, logger, time.Now)
				root := t.TempDir()
				dir := filepath.Join(root, ".mtix")
				require.NoError(t, os.Mkdir(dir, 0700))
				d := NewHooksDispatcher(s, dir, logger)
				if !empty {
					_, err = svc.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "survivor", Creator: "survivor"})
					require.NoError(t, err)
				}
				d.Dispatch(ctx)
				data, err := s.Export(ctx, "", "")
				require.NoError(t, err)
				_, err = svc.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "removed", Creator: "removed"})
				require.NoError(t, err)
				tail, err := s.JournalTail(ctx)
				require.NoError(t, err)
				if state == "cursor" {
					require.NoError(t, s.AdvanceHookScanFloorClamped(ctx, tail))
				} else {
					won, err := s.ClaimHookDispatch(ctx, "replace-file", tail, time.Minute)
					require.NoError(t, err)
					require.True(t, won)
					if state == "delivered" {
						require.NoError(t, s.RecordHookDispatchOutcome(ctx, "replace-file", tail, sqlite.OutcomeDelivered))
					}
				}
				require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(`hooks:
  - name: replace-file
    match: {events: [node.created]}
    deliver: [append-file]
    append-file: {path: capture.jsonl}
`), 0600))
				_, err = s.Import(ctx, data, sqlite.ImportModeReplace, empty)
				require.NoError(t, err)
				_, err = svc.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "fresh", Creator: "fresh"})
				require.NoError(t, err)
				reused, err := s.JournalTail(ctx)
				require.NoError(t, err)
				require.Equal(t, tail, reused)
				d.Dispatch(ctx)
				d.Dispatch(ctx)
				output, err := os.ReadFile(filepath.Join(root, "capture.jsonl"))
				require.NoError(t, err)
				lines := strings.Split(strings.TrimSpace(string(output)), "\n")
				require.Len(t, lines, 1)
				assert.Contains(t, lines[0], `"author":"fresh"`)
				assert.NotContains(t, string(output), `"author":"survivor"`)
				assert.NotContains(t, string(output), `"author":"removed"`)
			})
		}
	}
}
