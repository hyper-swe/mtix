// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	api "github.com/hyper-swe/mtix/internal/api/http"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type annotationActivityFixture struct {
	server *api.Server
	node   *model.Node
}

// This fixture uses only APIs that predate the projection, so reverting all
// production changes leaves this HTTP behavior witness compiling.
func newAnnotationActivityFixture(t *testing.T) annotationActivityFixture {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := sqlite.New(t.TempDir(), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	clock := func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
	nodes := service.NewNodeService(st, &service.NoopBroadcaster{}, &service.StaticConfig{}, logger, clock)
	prompts := service.NewPromptService(st, &service.NoopBroadcaster{}, logger, clock)
	node, err := nodes.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "Annotation reproduction", Creator: "human"})
	require.NoError(t, err)
	for _, recipient := range []string{"", "reader"} {
		require.NoError(t, prompts.AddAnnotation(ctx, node.ID, "Hello **reader**", "author", recipient))
	}
	saved, err := st.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, saved.Annotations, 2)
	srv := api.NewReadOnlyServer(api.ReadServices{Nodes: nodes}, logger, api.ServerConfig{Bind: "127.0.0.1", Port: "0"}, clock)
	t.Cleanup(func() { require.NoError(t, srv.Shutdown(ctx)) })
	return annotationActivityFixture{server: srv, node: saved}
}

func TestGetActivity_PlainAndAddressedAnnotations_AppearInActivity(t *testing.T) {
	f := newAnnotationActivityFixture(t)
	req := httptest.NewRequest("GET", "http://127.0.0.1/api/v1/nodes/"+f.node.ID+"/activity", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	out := httptest.NewRecorder()
	f.server.Router().ServeHTTP(out, req)
	require.Equal(t, 200, out.Code, out.Body.String())
	var response struct {
		Entries []map[string]any `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(out.Body.Bytes(), &response))
	t.Logf("persisted annotations=%d HTTP=%s", len(f.node.Annotations), out.Body.String())
	comments := map[string]map[string]any{}
	for _, entry := range response.Entries {
		if entry["type"] == "comment" {
			id, ok := entry["id"].(string)
			require.True(t, ok)
			comments[id] = entry
		}
	}
	require.Len(t, comments, 2, "both persisted annotations must appear in the real HTTP activity response")
	for _, ann := range f.node.Annotations {
		entry, exists := comments[ann.ID]
		require.True(t, exists)
		require.Equal(t, ann.Author, entry["author"])
		require.Equal(t, ann.Text, entry["text"])
		require.Equal(t, ann.CreatedAt.Format(time.RFC3339Nano), entry["created_at"])
		if ann.Addressee != "" {
			require.Equal(t, ann.Addressee, entry["addressee"])
		} else {
			require.NotContains(t, entry, "addressee")
		}
	}
}
