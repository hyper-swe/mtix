// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Webhook delivery tuning (FR-19.3). Attempts are bounded so a flaky endpoint
// can't retry forever, and every attempt is individually time-boxed so a hung
// server can't pin the async dispatcher.
const (
	webhookAttempts = 3
	webhookTimeout  = 10 * time.Second
	webhookBackoff  = 200 * time.Millisecond
)

// WebhookAdapter POSTs the journaled event JSON to a hook's configured URL
// (FR-19.3). It runs async after the mutation commits, so it is built to be
// non-fatal: it retries transient failures with backoff and only ever returns
// an error for the dispatcher to LOG — never to propagate to the mutation.
//
// Security: the target URL is read exclusively from the hook config
// (d.Hook.Webhook.URL). Event content (d.EventJSON) is sent as the body
// VERBATIM and is never interpolated into the URL, so a crafted event can't
// redirect a delivery (FR-19.3).
type WebhookAdapter struct {
	client  *http.Client
	backoff time.Duration // base delay between retries; grows per attempt
}

// NewWebhookAdapter returns an adapter using client, or a default client with a
// bounded timeout when client is nil. Each attempt is additionally wrapped in a
// per-request timeout, so even a caller-supplied client without a Timeout stays
// bounded.
func NewWebhookAdapter(client *http.Client) *WebhookAdapter {
	if client == nil {
		client = &http.Client{Timeout: webhookTimeout}
	}
	return &WebhookAdapter{client: client, backoff: webhookBackoff}
}

// Name implements Adapter.
func (a *WebhookAdapter) Name() string { return AdapterWebhook }

// Deliver POSTs d.EventJSON (Content-Type: application/json) to the configured
// URL, retrying transient failures up to webhookAttempts times with exponential
// backoff. It returns an error only after the final attempt fails.
func (a *WebhookAdapter) Deliver(ctx context.Context, d Delivery) error {
	if d.Hook.Webhook == nil || d.Hook.Webhook.URL == "" {
		return fmt.Errorf("webhook: hook %q has no webhook.url", d.Hook.Name)
	}
	url := d.Hook.Webhook.URL // config ONLY — never derived from event content

	var lastErr error
	for attempt := 0; attempt < webhookAttempts; attempt++ {
		if attempt > 0 {
			// Exponential backoff, but abandon promptly if the context is done.
			delay := a.backoff << (attempt - 1)
			select {
			case <-ctx.Done():
				return fmt.Errorf("webhook: %q canceled after %d attempt(s): %w", d.Hook.Name, attempt, ctx.Err())
			case <-time.After(delay):
			}
		}
		if lastErr = a.post(ctx, url, d.EventJSON); lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("webhook: %q failed after %d attempts: %w", d.Hook.Name, webhookAttempts, lastErr)
}

// post performs one time-boxed POST and reports a non-2xx status as an error so
// the caller retries it.
func (a *WebhookAdapter) post(ctx context.Context, url string, body []byte) error {
	reqCtx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: %s returned status %d", url, resp.StatusCode)
	}
	return nil
}
