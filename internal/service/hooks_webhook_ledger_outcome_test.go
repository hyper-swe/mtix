// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// webhookLedgerOutcome runs one dispatch pass for a webhook-only hook and
// returns the dispatch ledger outcome recorded for its event. A claim of an
// unrelated hook pins the scan floor so the terminal row is not pruned before
// it can be read (setup only, as in the exec ledger test).
func webhookLedgerOutcome(t *testing.T, hookURL string, seed func()) (string, []sqlite.HookLogEntry, *sqlite.Store) {
	t.Helper()
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	_, err := store.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = ?`, "0123456789abcdef", "meta.sync.machine_hash")
	require.NoError(t, err)
	if seed != nil {
		seed()
	}
	dir := t.TempDir()
	writeHooks(t, dir, webhookHookYAML(hookURL))
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "Webhook ledger", Creator: "worker"})
	require.NoError(t, err)
	firstSeq, err := store.JournalTail(ctx)
	require.NoError(t, err)
	won, err := store.ClaimHookDispatch(ctx, "in-flight-hook", firstSeq, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "worker"))
	doneSeq, err := store.JournalTail(ctx)
	require.NoError(t, err)

	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	var outcome string
	require.NoError(t, store.ReadDB().QueryRowContext(ctx, `
		SELECT outcome FROM hook_dispatch_ledger WHERE hook_name = ? AND event_seq = ?`,
		"notify-out", doneSeq).Scan(&outcome))
	return outcome, hookOutcomes(t, store, "notify-out"), store
}

// A refused webhook must settle the dispatch ledger as skipped-untrusted:
// never delivered (the event would look sent) and never error (retried).
func TestWebhookDispatch_RefusedOrigin_LedgerSkippedNeverDeliveredOrRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	cases := []struct {
		name  string
		allow func(*testing.T) // seeds consent for the test server
	}{
		{"unconsented origin", func(t *testing.T) { testutil.OperatorState(t) }},
		{"consented loopback without private opt-in", func(t *testing.T) {
			_, err := hooks.AllowOrigin(testutil.OperatorState(t), srv.URL, false, true, false)
			require.NoError(t, err)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, entries, _ := webhookLedgerOutcome(t, srv.URL+"/hook", func() { tc.allow(t) })
			require.Equal(t, sqlite.OutcomeSkippedUntrusted, outcome, "the ledger settles terminally as skipped")
			require.NotEqual(t, sqlite.OutcomeDelivered, outcome)
			require.NotEqual(t, sqlite.OutcomeError, outcome)
			require.Len(t, entries, 1, "a skip is recorded once and not retried by the second pass")
			require.Zero(t, atomic.LoadInt32(&hits))
		})
	}
}

// A dial-time refusal names the exact origin and the exact corrective command.
func TestWebhookDispatch_ConsentedLoopbackWithoutPrivate_DetailNamesExactOriginAndFix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	origin := "http://" + u.Host
	_, entries, _ := webhookLedgerOutcome(t, srv.URL+"/hook", func() {
		_, err := hooks.AllowOrigin(testutil.OperatorState(t), srv.URL, false, true, false)
		require.NoError(t, err)
	})
	require.Len(t, entries, 1)
	require.Contains(t, entries[0].Detail, hooks.OriginAllowFix(origin, true), "exact fix for the exact origin")
	require.NotContains(t, entries[0].Detail, "<origin>", "no placeholder fix")
}
