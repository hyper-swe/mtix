// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func webhookDelivery(url string, body []byte) Delivery {
	return Delivery{
		Hook:      Hook{Name: "notify", Webhook: &WebhookConfig{URL: url}},
		Event:     Event{Name: EventStatusChanged, NodeID: "HP-1.2"},
		EventJSON: body,
	}
}

func TestWebhook_PostsExactBodyToConfiguredURL(t *testing.T) {
	var gotBody []byte
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotType = r.Header.Get("Content-Type")
		require.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// EventJSON carries a decoy "url" — the adapter must ignore it and post to
	// the configured URL only (FR-19.3 security).
	body := []byte(`{"event":"status.changed","url":"http://attacker.example/steal"}`)
	a := NewWebhookAdapter(nil)
	require.NoError(t, a.Deliver(context.Background(), webhookDelivery(srv.URL, body)))

	require.Equal(t, body, gotBody, "posts EventJSON verbatim")
	require.Equal(t, "application/json", gotType)
}

func TestWebhook_RetriesOn5xxThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewWebhookAdapter(nil)
	a.backoff = time.Millisecond // keep the test fast
	require.NoError(t, a.Deliver(context.Background(), webhookDelivery(srv.URL, []byte(`{}`))))
	require.Equal(t, int32(3), atomic.LoadInt32(&calls), "two 5xx retries then a success")
}

func TestWebhook_ErrorsAfterThreeFailures(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	a := NewWebhookAdapter(nil)
	a.backoff = time.Millisecond
	err := a.Deliver(context.Background(), webhookDelivery(srv.URL, []byte(`{}`)))
	require.Error(t, err, "returns the final failure for the dispatcher to log")
	require.Equal(t, int32(webhookAttempts), atomic.LoadInt32(&calls), "exactly 3 attempts, no more")
}

func TestWebhook_MissingURLErrors(t *testing.T) {
	a := NewWebhookAdapter(nil)
	err := a.Deliver(context.Background(), Delivery{Hook: Hook{Name: "notify"}})
	require.Error(t, err)
}
