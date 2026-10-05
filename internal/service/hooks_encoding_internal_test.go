// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests hook payload encoding, delivery suppression, and terminal ledger errors
// for MTIX-118 without exposing a public encoder API.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type encodingCaptureAdapter struct {
	name       string
	deliveries []hooks.Delivery
}

func (a *encodingCaptureAdapter) Name() string { return a.name }
func (a *encodingCaptureAdapter) Deliver(_ context.Context, d hooks.Delivery) error {
	a.deliveries = append(a.deliveries, d)
	return nil
}

func newEncodingDispatcher(t *testing.T, log *bytes.Buffer) *HooksDispatcher {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(log, nil))
	store, err := sqlite.New(filepath.Join(t.TempDir(), "test.db"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return NewHooksDispatcher(store, t.TempDir(), logger)
}

func TestHooksDispatcher_EncoderError_RecordsErrorWithoutDelivery(t *testing.T) {
	for _, tt := range []struct {
		name    string
		payload []byte
	}{{"no bytes", nil}, {"partial bytes", []byte(`{"partial":true}`)}} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			d := newEncodingDispatcher(t, &logs)
			cause := errors.New("forced encoding failure")
			calls := 0
			d.encodeEvent = func(any) ([]byte, error) { calls++; return tt.payload, cause }
			adapters := []*encodingCaptureAdapter{{name: hooks.AdapterInbox}, {name: hooks.AdapterExec}, {name: hooks.AdapterWebhook}, {name: hooks.AdapterAppendFile}}
			d.registry = hooks.NewRegistry(adapters[0], adapters[1], adapters[2], adapters[3])
			h := hooks.Hook{Name: "encode-failure", Deliver: []string{hooks.AdapterInbox, hooks.AdapterExec, hooks.AdapterWebhook, hooks.AdapterAppendFile}}
			je := sqlite.JournalEvent{Seq: 42}
			evt := hooks.Event{Seq: je.Seq, Name: hooks.EventNodeCreated, NodeID: "TEST-1"}
			ctx := context.Background()
			won, err := d.store.ClaimHookDispatch(ctx, h.Name, je.Seq, hookClaimLease)
			require.NoError(t, err)
			require.True(t, won)

			d.finishClaim(ctx, h, evt, je, true, hooks.ExecDispatchAny)

			assert.Equal(t, 1, calls)
			for _, adapter := range adapters {
				assert.Empty(t, adapter.deliveries, adapter.name)
			}
			var outcome string
			// Inspect the claimed pair before scan-floor compaction removes terminal rows.
			require.NoError(t, d.store.ReadDB().QueryRowContext(ctx, `SELECT outcome FROM hook_dispatch_ledger WHERE hook_name = ? AND event_seq = ?`, h.Name, je.Seq).Scan(&outcome))
			assert.Equal(t, sqlite.OutcomeError, outcome)
			var record map[string]any
			require.NoError(t, json.NewDecoder(&logs).Decode(&record))
			assert.Equal(t, "ERROR", record["level"])
			assert.Equal(t, h.Name, record["hook"])
			assert.Equal(t, float64(je.Seq), record["seq"])
			assert.Contains(t, record["error"], cause.Error())
			audit, err := d.store.ReadHookLog(ctx, 10)
			require.NoError(t, err)
			require.Len(t, audit, 1)
			assert.Equal(t, sqlite.OutcomeError, audit[0].Outcome)
			assert.Contains(t, audit[0].Detail, cause.Error())
			won, err = d.store.ClaimHookDispatch(ctx, h.Name, je.Seq, hookClaimLease)
			require.NoError(t, err)
			assert.False(t, won, "encoding failure is terminal and must not retry")
		})
	}
}

func TestHooksDispatcher_DefaultEncoder_DeliversCompletePayload(t *testing.T) {
	var logs bytes.Buffer
	d := newEncodingDispatcher(t, &logs)
	adapter := &encodingCaptureAdapter{name: hooks.AdapterWebhook}
	d.registry = hooks.NewRegistry(adapter)
	h := hooks.Hook{Name: "encoded", Deliver: []string{adapter.name}}
	je := sqlite.JournalEvent{Seq: 7}
	evt := hooks.Event{Seq: je.Seq, Name: hooks.EventStatusChanged, NodeID: "TEST-1", Author: "worker", ToAgent: "reviewer", StatusTo: "done", Synced: true}
	require.Equal(t, sqlite.OutcomeDelivered, d.fire(context.Background(), h, evt, je, true, hooks.ExecDispatchAny))
	require.Len(t, adapter.deliveries, 1)
	assert.Equal(t, h, adapter.deliveries[0].Hook)
	assert.Equal(t, evt, adapter.deliveries[0].Event)
	assert.JSONEq(t, `{"seq":7,"event":"status.changed","node_id":"TEST-1","author":"worker","to":"reviewer","status":"done","synced":true,"hook":"encoded"}`, string(adapter.deliveries[0].EventJSON))
}
