// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// progressSpyStore counts subtree and child reads and can fail the subtree read.
type progressSpyStore struct {
	store.Store
	treeCalls, childCalls int
	treeErr               error
}

func (s *progressSpyStore) GetTree(ctx context.Context, id string, d int) ([]*model.Node, error) {
	s.treeCalls++
	if s.treeErr != nil {
		return nil, s.treeErr
	}
	return s.Store.GetTree(ctx, id, d)
}

func (s *progressSpyStore) GetDirectChildren(ctx context.Context, id string) ([]*model.Node, error) {
	s.childCalls++
	return s.Store.GetDirectChildren(ctx, id)
}

func progressSpyServer(t *testing.T) (*Server, *sqlite.Store, *progressSpyStore, *service.NodeService) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	st, err := sqlite.New(dir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	spy := &progressSpyStore{Store: st}
	nodes := service.NewNodeService(spy, &service.NoopBroadcaster{}, &service.StaticConfig{}, logger, testClock())
	configSvc, err := service.NewConfigService("")
	require.NoError(t, err)
	svcs := Services{
		Read:  ReadServices{Nodes: nodes, Config: configSvc},
		Write: WriteServices{Nodes: nodes, Config: configSvc},
	}
	return NewServer(svcs, logger, ServerConfig{Bind: "127.0.0.1", Port: "0"}, testClock()), st, spy, nodes
}

func progressGet(t *testing.T, s *Server, id string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, newLocalRequest(http.MethodGet, "/api/v1/progress/"+id, nil))
	return w
}

func progressMk(t *testing.T, n *service.NodeService, parent, title string) string {
	t.Helper()
	node, err := n.CreateNode(t.Context(), &service.CreateNodeRequest{
		Title: title, ParentID: parent, Project: "TEST", Creator: "tester"})
	require.NoError(t, err)
	return node.ID
}

// TestNodeProgress_InvalidatedGrandchild_ReportsDescendantFlag covers FR-5.6a.
func TestNodeProgress_InvalidatedGrandchild_ReportsDescendantFlag(t *testing.T) {
	s, st, _, nodes := progressSpyServer(t)
	root := progressMk(t, nodes, "", "root")
	leaf := progressMk(t, nodes, root, "done leaf")
	mid := progressMk(t, nodes, root, "mid")
	grand := progressMk(t, nodes, mid, "grand")
	require.NoError(t, nodes.TransitionStatus(t.Context(), leaf, model.StatusInProgress, "", "tester"))
	require.NoError(t, nodes.TransitionStatus(t.Context(), leaf, model.StatusDone, "", "tester"))
	require.NoError(t, st.TransitionStatus(t.Context(), grand, model.StatusInvalidated, "r", "tester"))

	w := progressGet(t, s, root)
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(1), resp["invalidated_count"])
	assert.Equal(t, true, resp["has_invalidated_descendants"])
	assert.Equal(t, false, resp["all_children_excluded"])
}

// TestNodeProgress_AllChildrenExcluded_ReportsFlag covers FR-5.6b.
func TestNodeProgress_AllChildrenExcluded_ReportsFlag(t *testing.T) {
	s, st, _, nodes := progressSpyServer(t)
	root := progressMk(t, nodes, "", "root")
	a := progressMk(t, nodes, root, "a")
	b := progressMk(t, nodes, root, "b")
	require.NoError(t, nodes.TransitionStatus(t.Context(), a, model.StatusCancelled, "descoped", "tester"))
	require.NoError(t, st.TransitionStatus(t.Context(), b, model.StatusInvalidated, "r", "tester"))

	w := progressGet(t, s, root)
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["all_children_excluded"])
	assert.Equal(t, float64(0), resp["progress"])
}

// TestNodeProgress_ChildReadError_IsErrorNotZero: a failed enumeration is a 5xx.
func TestNodeProgress_ChildReadError_IsErrorNotZero(t *testing.T) {
	s, _, spy, nodes := progressSpyServer(t)
	root := progressMk(t, nodes, "", "root")
	progressMk(t, nodes, root, "kid")
	spy.treeErr = errors.New("injected child read failure")

	w := progressGet(t, s, root)
	assert.GreaterOrEqual(t, w.Code, http.StatusInternalServerError)
	assert.NotContains(t, w.Body.String(), `"progress"`)
}

// TestNodeProgress_OneSnapshot_SingleStoreRead: the handler reads the subtree once.
func TestNodeProgress_OneSnapshot_SingleStoreRead(t *testing.T) {
	s, _, spy, nodes := progressSpyServer(t)
	root := progressMk(t, nodes, "", "root")
	progressMk(t, nodes, root, "kid")
	spy.treeCalls, spy.childCalls = 0, 0

	require.Equal(t, http.StatusOK, progressGet(t, s, root).Code)
	assert.Equal(t, 1, spy.treeCalls)
	assert.Zero(t, spy.childCalls)
}
