// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests real dispatcher reads across one- and two-event journal rewinds.
package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFloorCaptureHook(t *testing.T, d *HooksDispatcher) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(d.mtixDir, "hooks.yaml"), []byte(`hooks:
  - name: capture
    match: {events: [node.created]}
    deliver: [append-file]
    append-file: {path: capture.jsonl}
`), 0600))
}

func hookFloorReadAcrossReplace(t *testing.T, dropped int) (*HooksDispatcher, *encodingCaptureAdapter) {
	t.Helper()
	ctx := context.Background()
	d := newEncodingDispatcher(t, new(bytes.Buffer))
	nodes := NewNodeService(d.store, nil, nil, d.logger, time.Now)
	_, err := nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "survivor", Creator: "survivor"})
	require.NoError(t, err)
	board, err := d.store.Export(ctx, "", "")
	require.NoError(t, err)
	for range dropped {
		_, err = nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "removed", Creator: "removed"})
		require.NoError(t, err)
	}
	oldTail, err := d.store.JournalTail(ctx)
	require.NoError(t, err)
	require.NoError(t, d.store.AdvanceHookScanFloorClamped(ctx, oldTail))
	writeFloorCaptureHook(t, d)
	capture := &encodingCaptureAdapter{name: hooks.AdapterAppendFile}
	d.registry = hooks.NewRegistry(capture)
	d.readJournal = func(ctx context.Context, floor int64, limit int) ([]sqlite.JournalEvent, error) {
		require.Equal(t, oldTail, floor)
		_, err := d.store.Import(ctx, board, sqlite.ImportModeReplace, false)
		require.NoError(t, err)
		surviving, err := d.store.JournalTail(ctx)
		require.NoError(t, err)
		require.Equal(t, int64(dropped), floor-surviving, "replace must drop exactly the chosen number of events")
		for i := range dropped + 1 {
			_, err = nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "fresh", Creator: fmt.Sprintf("fresh%d", i)})
			require.NoError(t, err)
		}
		return d.store.ReadJournalSince(ctx, floor, limit)
	}
	return d, capture
}

func TestHooksReplace_FloorReadHandlesOneAndTwoEventRewinds(t *testing.T) {
	for _, dropped := range []int{1, 2} {
		t.Run(fmt.Sprintf("dropped=%d", dropped), func(t *testing.T) {
			ctx := context.Background()
			d, capture := hookFloorReadAcrossReplace(t, dropped)
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
			expected := make(map[string]int)
			for i := range dropped + 1 {
				expected[fmt.Sprintf("fresh%d", i)] = 1
			}
			assert.Equal(t, expected, authors)
		})
	}
}
