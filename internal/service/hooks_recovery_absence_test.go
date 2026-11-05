// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests recovery when a claimed journal identity can no longer be read.
package service

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHooksRecovery_EventRemovedAfterReclaimIsQuiet(t *testing.T) {
	ctx := context.Background()
	d := newEncodingDispatcher(t, new(bytes.Buffer))
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
	// Remove the event after the transaction's claim fence but before re-read.
	_, err = d.store.WriteDB().Exec(`CREATE TRIGGER remove_after_reclaim AFTER UPDATE OF fired_at ON hook_dispatch_ledger BEGIN DELETE FROM sync_events WHERE rowid=NEW.event_seq; END`)
	require.NoError(t, err)
	capture := &encodingCaptureAdapter{name: hooks.AdapterAppendFile}
	d.registry = hooks.NewRegistry(capture)
	cfg := hooks.Config{Hooks: []hooks.Hook{{Name: "capture", Deliver: []string{hooks.AdapterAppendFile}}}}
	d.refireStaleClaim(ctx, cfg, claims[0], nil, hooks.ExecDispatchAny)
	assert.Empty(t, capture.deliveries)
	assert.Equal(t, uint64(1), d.HookIdentityFenceMisses())
	audit, err := d.store.ReadHookLog(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, audit, "a replaced event is not a failed recovery")
	var outcome string
	require.NoError(t, d.store.ReadDB().QueryRow(`SELECT outcome FROM hook_dispatch_ledger WHERE hook_name='capture' AND event_seq=?`, event.Seq).Scan(&outcome))
	assert.Equal(t, sqlite.OutcomeClaimed, outcome, "leave the orphan for the existing lease cleanup")
}

func TestHooksRecovery_AbandonCannotFinalizeReplacementAfterReadFailure(t *testing.T) {
	ctx := context.Background()
	d := newEncodingDispatcher(t, new(bytes.Buffer))
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
	// Reuse the sequence after the guarded reclaim, then force the subsequent
	// read to fail. The old abandonment must still fence its outcome write.
	replacementID := "10000000-0000-7000-8000-000000000001"
	require.NotEqual(t, event.EventID, replacementID)
	_, err = d.store.WriteDB().Exec(`CREATE TRIGGER replace_before_failed_read AFTER UPDATE OF fired_at ON hook_dispatch_ledger BEGIN UPDATE sync_events SET event_id='10000000-0000-7000-8000-000000000001' WHERE rowid=NEW.event_seq; END`)
	require.NoError(t, err)
	require.NoError(t, d.store.ReadDB().Close())
	capture := &encodingCaptureAdapter{name: hooks.AdapterAppendFile}
	d.registry = hooks.NewRegistry(capture)
	cfg := hooks.Config{Hooks: []hooks.Hook{{Name: "capture", Deliver: []string{hooks.AdapterAppendFile}}}}
	d.refireStaleClaim(ctx, cfg, claims[0], nil, hooks.ExecDispatchAny)
	var actualID, outcome string
	require.NoError(t, d.store.WriteDB().QueryRow(`SELECT event_id FROM sync_events WHERE rowid=?`, event.Seq).Scan(&actualID))
	require.Equal(t, replacementID, actualID, "the identity changed inside the reclaim transaction")
	require.NoError(t, d.store.WriteDB().QueryRow(`SELECT outcome FROM hook_dispatch_ledger WHERE hook_name='capture' AND event_seq=?`, event.Seq).Scan(&outcome))
	assert.Equal(t, sqlite.OutcomeClaimed, outcome, "a failed old read cannot settle the replacement event's claim")
	assert.Equal(t, uint64(1), d.HookIdentityFenceMisses())
	assert.Empty(t, capture.deliveries)
}
