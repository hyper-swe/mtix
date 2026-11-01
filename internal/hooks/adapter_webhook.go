// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
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
// Security (W15, TM I35): the target URL is read exclusively from the hook
// config and is never derived from event content. A delivery goes out only to
// an origin the operator consented to on this host; loopback, link-local and
// private destinations additionally need an explicit private opt-in, checked
// against the address actually dialed; redirects reach only consented origins.
// A refusal is *OriginRefusedError, final and never retried.
type WebhookAdapter struct {
	source  OriginSource
	backoff time.Duration // base delay between retries; grows per attempt
	// lookup resolves hostnames; nil uses the system resolver (tests inject).
	lookup func(context.Context, string) ([]net.IP, error)
}

// NewWebhookAdapter returns an adapter bound to the operator-local consent
// source. A nil source consents to nothing, so every delivery is refused.
func NewWebhookAdapter(source OriginSource) *WebhookAdapter {
	return &WebhookAdapter{source: source, backoff: webhookBackoff}
}

// Name implements Adapter.
func (a *WebhookAdapter) Name() string { return AdapterWebhook }

// Deliver POSTs d.EventJSON (Content-Type: application/json) to the configured
// URL after the consent check, retrying transient failures up to
// webhookAttempts times with exponential backoff. It returns an error only
// after the final attempt fails, or immediately on a consent refusal.
func (a *WebhookAdapter) Deliver(ctx context.Context, d Delivery) error {
	if d.Hook.Webhook == nil || d.Hook.Webhook.URL == "" {
		return fmt.Errorf("webhook: hook %q has no webhook.url", d.Hook.Name)
	}
	url := d.Hook.Webhook.URL // config ONLY — never derived from event content
	list, err := a.consent(url)
	if err != nil {
		return err
	}
	client := a.guardedClient(list)

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
		if lastErr = a.post(ctx, client, url, d.EventJSON); lastErr == nil {
			return nil
		}
		var refused *OriginRefusedError
		if errors.As(lastErr, &refused) {
			return fmt.Errorf("webhook: %q: %w", d.Hook.Name, lastErr)
		}
	}
	return fmt.Errorf("webhook: %q failed after %d attempts: %w", d.Hook.Name, webhookAttempts, lastErr)
}

// consent returns the consent list when the URL's origin is listed, and an
// *OriginRefusedError otherwise (including when the list cannot be read).
func (a *WebhookAdapter) consent(rawURL string) (OriginList, error) {
	origin, err := NormalizeOrigin(rawURL)
	if err != nil {
		return OriginList{}, err
	}
	if a.source == nil {
		return OriginList{}, &OriginRefusedError{Origin: origin, Reason: "no consent source configured", Fix: OriginAllowFix(origin, false)}
	}
	list, err := a.source()
	if err != nil {
		return OriginList{}, &OriginRefusedError{Origin: origin, Reason: "consent state unreadable: " + err.Error(), Fix: originResetFix()}
	}
	if _, ok := list.Find(origin); !ok {
		return OriginList{}, &OriginRefusedError{Origin: origin, Reason: "origin not consented on this host", Fix: OriginAllowFix(origin, privateHost(hostOf(origin)))}
	}
	return list, nil
}

// post performs one time-boxed POST and reports a non-2xx status as an error so
// the caller retries it.
func (a *WebhookAdapter) post(ctx context.Context, client *http.Client, url string, body []byte) error {
	reqCtx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
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
