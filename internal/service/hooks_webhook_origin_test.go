// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func webhookHookYAML(url string) string {
	return fmt.Sprintf(`
hooks:
  - name: notify-out
    match:
      events: [status.changed]
      status-to: [done]
    deliver: [webhook]
    webhook:
      url: %s
`, url)
}

func hookOutcomes(t *testing.T, store *sqlite.Store, hook string) []sqlite.HookLogEntry {
	t.Helper()
	entries, err := store.ReadHookLog(context.Background(), 1000)
	require.NoError(t, err)
	var out []sqlite.HookLogEntry
	for _, e := range entries {
		if e.Hook == hook {
			out = append(out, e)
		}
	}
	return out
}

func TestWebhookDispatch_UnconsentedOrigin_SkippedWithFixNotDelivered(t *testing.T) {
	testutil.OperatorState(t)
	svc, store, _ := newTestNodeService(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	dir := t.TempDir()
	writeHooks(t, dir, webhookHookYAML(srv.URL+"/hook"))

	makeDoneEvent(t, svc)
	d := service.NewHooksDispatcher(store, dir, slog.Default())
	d.Dispatch(context.Background())
	d.Dispatch(context.Background())

	require.Zero(t, atomic.LoadInt32(&hits), "no request leaves for an unconsented origin")
	entries := hookOutcomes(t, store, "notify-out")
	require.Len(t, entries, 1, "the skip is terminal and recorded once")
	require.Equal(t, "skipped-untrusted", entries[0].Outcome)
	require.Equal(t, "webhook", entries[0].Adapter)
	require.Contains(t, entries[0].Detail, "mtix hooks allow-origin")
	require.Zero(t, deliveredCount(t, store, "notify-out"))
}

func TestWebhookDispatch_ConsentedPrivateOrigin_Delivered(t *testing.T) {
	state := testutil.OperatorState(t)
	svc, store, _ := newTestNodeService(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	_, err := hooks.AllowOrigin(state, srv.URL, true, true, false)
	require.NoError(t, err)
	dir := t.TempDir()
	writeHooks(t, dir, webhookHookYAML(srv.URL+"/hook"))

	makeDoneEvent(t, svc)
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(context.Background())

	require.Equal(t, int32(1), atomic.LoadInt32(&hits))
	require.Equal(t, 1, deliveredCount(t, store, "notify-out"))
}

func TestWebhookDispatch_ConsentedLoopbackWithoutPrivate_SkippedNotError(t *testing.T) {
	state := testutil.OperatorState(t)
	svc, store, _ := newTestNodeService(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	_, err := hooks.AllowOrigin(state, srv.URL, false, true, false)
	require.NoError(t, err)
	dir := t.TempDir()
	writeHooks(t, dir, webhookHookYAML(srv.URL+"/hook"))

	makeDoneEvent(t, svc)
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(context.Background())

	require.Zero(t, atomic.LoadInt32(&hits))
	entries := hookOutcomes(t, store, "notify-out")
	require.Len(t, entries, 1)
	require.Equal(t, "skipped-untrusted", entries[0].Outcome)
	require.Contains(t, entries[0].Detail, "--allow-private")
}
