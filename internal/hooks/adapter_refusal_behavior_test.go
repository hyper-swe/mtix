// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

// These tests cover owned adapter refusal boundaries without altering retries.
import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAppendFile_InvalidConfiguration_RefusesWithoutWrites(t *testing.T) {
	for _, name := range []string{"absent", "empty"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			sentinel := filepath.Join(dir, "sentinel")
			require.NoError(t, os.WriteFile(sentinel, []byte("unchanged"), 0o600))
			delivery := appendDelivery("", Event{Name: EventStatusChanged}, []byte(`{}`))
			if name == "absent" {
				delivery.Hook.AppendFile = nil
			}
			err := NewAppendFileAdapter(dir).Deliver(context.Background(), delivery)
			require.EqualError(t, err, `append-file: hook "audit" has no append-file.path`)
			assertRefusalDirectory(t, dir, []string{"sentinel"})
			contents, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			require.Equal(t, "unchanged", string(contents))
		})
	}
}

func TestAppendFile_DirectoryDestination_PreservesOwnedFiles(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "target")
	require.NoError(t, os.Mkdir(destination, 0o700))
	sentinel := filepath.Join(destination, "sentinel")
	require.NoError(t, os.WriteFile(sentinel, []byte("unchanged"), 0o600))
	err := NewAppendFileAdapter(dir).Deliver(context.Background(), appendDelivery("target", Event{Name: EventStatusChanged}, []byte(`{}`)))
	require.ErrorContains(t, err, "not a regular file")
	assertRefusalDirectory(t, dir, []string{"target"})
	assertRefusalDirectory(t, destination, []string{"sentinel"})
	contents, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, "unchanged", string(contents))
}

func assertRefusalDirectory(t *testing.T, dir string, want []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	require.Equal(t, want, names)
}

func TestWebhook_CanceledCaller_RefusesWithoutRequest(t *testing.T) {
	server, requests := deliveryServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cancel()
	err := newTestAdapter(listSource(originOf(t, server.URL, true))).Deliver(ctx, webhookDelivery(server.URL, []byte(`{"event":"status.changed"}`)))
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, `webhook: "notify" canceled after 1 attempt(s)`)
	require.Empty(t, requests.snapshot())
}

func TestWebhook_MalformedURL_RefusedBeforeAnyAttempt(t *testing.T) {
	server, requests := deliveryServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, ctx.Err())
	err := newTestAdapter(listSource(originOf(t, server.URL, true))).Deliver(ctx, webhookDelivery(server.URL+"/invalid\npath", []byte(`{}`)))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "attempts", "an invalid URL is rejected before any attempt")
	var urlError *url.Error
	require.ErrorAs(t, err, &urlError)
	require.Equal(t, "parse", urlError.Op)
	require.ErrorContains(t, urlError.Err, "invalid control character in URL")
	require.Empty(t, requests.snapshot())
	require.NoError(t, ctx.Err())
}
