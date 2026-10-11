// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

// These tests cover loaded selection and real registry delivery effects.
import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type deliveryRequest struct {
	method, contentType string
	body                []byte
	err                 error
}

type deliveryRequests struct {
	mu       sync.Mutex
	requests []deliveryRequest
}

func (r *deliveryRequests) snapshot() []deliveryRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]deliveryRequest(nil), r.requests...)
}

func deliveryServer(t *testing.T) (*httptest.Server, *deliveryRequests) {
	t.Helper()
	requests := &deliveryRequests{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		requests.mu.Lock()
		requests.requests = append(requests.requests, deliveryRequest{r.Method, r.Header.Get("Content-Type"), body, err})
		requests.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func loadedDeliveryConfig(t *testing.T, dir, url string) Config {
	t.Helper()
	yaml := fmt.Sprintf(`hooks:
  - name: z-audit
    match: {events: [status.changed], under: HP-1, status-to: [done], from-agent-not: worker-self}
    deliver: [append-file]
    append-file: {path: z.log}
  - name: sibling
    match: {events: [status.changed], under: HP-12}
    deliver: [append-file]
    append-file: {path: sibling.log}
  - name: notify
    match: {events: [status.changed], under: HP-1, status-to: [done]}
    deliver: [webhook]
    webhook: {url: %q}
  - name: a-audit
    match: {events: [status.changed], under: HP-1, status-to: [done]}
    deliver: [append-file]
    append-file: {path: a.log}
  - name: open-only
    match: {events: [status.changed], under: HP-1, status-to: [open]}
    deliver: [append-file]
    append-file: {path: open.log}
`, url)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(yaml), 0o600))
	cfg, warnings := Load(dir)
	require.Empty(t, warnings)
	require.Equal(t, []string{"z-audit", "sibling", "notify", "a-audit", "open-only"}, deliveryHookNames(cfg.Hooks))
	return cfg
}

func deliveryHookNames(hooks []Hook) []string {
	names := make([]string, len(hooks))
	for i, hook := range hooks {
		names[i] = hook.Name
	}
	return names
}

func TestMatchingHooks_ConfigFilters_SelectsExactOrderedHooks(t *testing.T) {
	server, _ := deliveryServer(t)
	cfg := loadedDeliveryConfig(t, t.TempDir(), server.URL)
	base := Event{Name: EventStatusChanged, NodeID: "HP-1.2", StatusTo: "done", Author: "worker"}
	cases := []struct {
		name  string
		event Event
		want  []string
	}{
		{"matching", base, []string{"z-audit", "notify", "a-audit"}},
		{"synced", mut(base, func(e *Event) { e.Synced = true }), []string{"z-audit", "notify", "a-audit"}},
		{"sibling prefix", mut(base, func(e *Event) { e.NodeID = "HP-12" }), []string{"sibling"}},
		{"other status", mut(base, func(e *Event) { e.StatusTo = "open" }), []string{"open-only"}},
		{"self origin", mut(base, func(e *Event) { e.Author = "worker-self" }), []string{"notify", "a-audit"}},
		{"via same hook", mut(base, func(e *Event) { e.ViaHook = "notify" }), []string{"z-audit", "a-audit"}},
		{"wrong event", mut(base, func(e *Event) { e.Name = EventNodeCreated }), []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, deliveryHookNames(cfg.MatchingHooks(tc.event)))
		})
	}
}

func TestLoad_ValidConfiguration_RoutesOrderedDeliveries(t *testing.T) {
	dir := t.TempDir()
	server, requests := deliveryServer(t)
	cfg := loadedDeliveryConfig(t, dir, server.URL)
	appendAdapter, webhookAdapter := NewAppendFileAdapter(dir), NewWebhookAdapter(server.Client())
	registry := NewRegistry(nil, appendAdapter, webhookAdapter)
	for key, want := range map[string]Adapter{AdapterAppendFile: appendAdapter, AdapterWebhook: webhookAdapter} {
		got, ok := registry.Lookup(key)
		require.True(t, ok)
		require.Same(t, want, got)
	}
	absent, ok := registry.Lookup("unregistered")
	require.False(t, ok)
	require.Nil(t, absent)
	for _, name := range []string{"z.log", "a.log"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("prior\n"), 0o600))
	}
	event := Event{Name: EventStatusChanged, NodeID: "HP-1.2", StatusTo: "done", Author: "worker"}
	bodies := [][]byte{[]byte(`{"n":1,"url":"http://unused.invalid/decoy"}`), []byte(`{"n":2,"text":"quotes: \"ok\""}`)}
	for _, body := range bodies {
		deliverSelectedHooks(t, registry, cfg, event, body)
	}
	for _, name := range []string{"z.log", "a.log"} {
		assertDeliveryLines(t, filepath.Join(dir, name), event, bodies)
	}
	assertDeliveryRequests(t, requests.snapshot(), bodies)
	for _, name := range []string{"sibling.log", "open.log"} {
		_, err := os.Stat(filepath.Join(dir, name))
		require.True(t, os.IsNotExist(err))
	}
}

func deliverSelectedHooks(t *testing.T, registry *Registry, cfg Config, event Event, body []byte) {
	t.Helper()
	selected := cfg.MatchingHooks(event)
	require.Equal(t, []string{"z-audit", "notify", "a-audit"}, deliveryHookNames(selected))
	for _, hook := range selected {
		for _, key := range hook.Deliver {
			adapter, ok := registry.Lookup(key)
			require.True(t, ok)
			require.NoError(t, adapter.Deliver(context.Background(), Delivery{Hook: hook, Event: event, EventJSON: body}))
		}
	}
}

func assertDeliveryLines(t *testing.T, path string, event Event, bodies [][]byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	require.Len(t, lines, len(bodies)+2)
	require.Equal(t, "prior", lines[0])
	require.Empty(t, lines[len(lines)-1])
	for i, body := range bodies {
		fields := strings.SplitN(lines[i+1], "\t", 4)
		require.Len(t, fields, 4)
		_, err := time.Parse(time.RFC3339, fields[0])
		require.NoError(t, err)
		require.Equal(t, []string{event.Name, event.NodeID, string(body)}, fields[1:])
	}
}

func assertDeliveryRequests(t *testing.T, requests []deliveryRequest, bodies [][]byte) {
	t.Helper()
	require.Len(t, requests, len(bodies))
	for i, request := range requests {
		require.NoError(t, request.err)
		require.Equal(t, http.MethodPost, request.method)
		require.Equal(t, "application/json", request.contentType)
		require.Equal(t, bodies[i], request.body)
	}
}

func TestLoad_InvalidHook_IsolatesWarningAndKeepsValidNeighbors(t *testing.T) {
	cases := []struct{ name, middle, warning string }{
		{"duplicate", "name: before\n    match: {events: [status.changed]}\n    deliver: [inbox]", `hook "before" disabled: duplicate name`},
		{"events", "name: bad\n    deliver: [inbox]", `hook "bad" disabled: match.events is required (subscribe to at least one event)`},
		{"delivery", "name: bad\n    match: {events: [status.changed]}", `hook "bad" disabled: deliver is required (name at least one adapter)`},
		{"unknown", "name: bad\n    match: {events: [status.changed]}\n    deliver: [unknown]", `hook "bad" disabled: unknown deliver adapter "unknown"`},
		{"webhook absent", "name: bad\n    match: {events: [status.changed]}\n    deliver: [webhook]", `hook "bad" disabled: webhook adapter requires webhook.url`},
		{"webhook empty", "name: bad\n    match: {events: [status.changed]}\n    deliver: [webhook]\n    webhook: {url: ''}", `hook "bad" disabled: webhook adapter requires webhook.url`},
		{"append absent", "name: bad\n    match: {events: [status.changed]}\n    deliver: [append-file]", `hook "bad" disabled: append-file adapter requires append-file.path`},
		{"append empty", "name: bad\n    match: {events: [status.changed]}\n    deliver: [append-file]\n    append-file: {path: ''}", `hook "bad" disabled: append-file adapter requires append-file.path`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			before := "hooks:\n  - name: before\n    match: {events: [status.changed]}\n    deliver: [append-file]\n    append-file: {path: before.log}\n  - "
			after := "\n  - name: after\n    match: {events: [status.changed]}\n    deliver: [append-file]\n    append-file: {path: after.log}\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(before+tc.middle+after), 0o600))
			cfg, warnings := Load(dir)
			require.Equal(t, []string{tc.warning}, warnings)
			require.Equal(t, []string{"before", "after"}, deliveryHookNames(cfg.Hooks))
			event := Event{Name: EventStatusChanged, NodeID: "HP-1.2"}
			require.Equal(t, []string{"before", "after"}, deliveryHookNames(cfg.MatchingHooks(event)))
			registry := NewRegistry(NewAppendFileAdapter(dir))
			for _, hook := range cfg.MatchingHooks(event) {
				adapter, ok := registry.Lookup(hook.Deliver[0])
				require.True(t, ok)
				require.NoError(t, adapter.Deliver(context.Background(), Delivery{Hook: hook, Event: event, EventJSON: []byte(`{}`)}))
			}
			assertValidatedDeliveryFiles(t, dir)
		})
	}
}

func assertValidatedDeliveryFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	require.Equal(t, []string{"after.log", "before.log", "hooks.yaml"}, names)
	for _, name := range []string{"before.log", "after.log"} {
		assertDeliveryLinesWithoutPrior(t, filepath.Join(dir, name))
	}
}

func assertDeliveryLinesWithoutPrior(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	require.Len(t, lines, 2)
	fields := strings.SplitN(lines[0], "\t", 4)
	require.Len(t, fields, 4)
	_, err = time.Parse(time.RFC3339, fields[0])
	require.NoError(t, err)
	require.Equal(t, []string{EventStatusChanged, "HP-1.2", `{}`}, fields[1:])
	require.Empty(t, lines[1])
}

func TestLoad_UnreadableOrMalformedFile_DisablesConfiguration(t *testing.T) {
	for _, name := range []string{"malformed", "directory"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "hooks.yaml")
			prefix := "hooks.yaml: parse failed, hooks disabled: "
			if name == "directory" {
				require.NoError(t, os.Mkdir(path, 0o700))
				prefix = "hooks.yaml: read failed, hooks disabled: "
			} else {
				require.NoError(t, os.WriteFile(path, []byte("hooks: ["), 0o600))
			}
			cfg, warnings := Load(dir)
			require.Empty(t, cfg.Hooks)
			require.Len(t, warnings, 1)
			require.True(t, strings.HasPrefix(warnings[0], prefix))
			require.NotEqual(t, prefix, warnings[0])
			require.Empty(t, cfg.MatchingHooks(Event{Name: EventStatusChanged}))
		})
	}
}
