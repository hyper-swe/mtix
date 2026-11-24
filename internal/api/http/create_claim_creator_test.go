// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// REST create and decompose forward the X-Agent-ID author; an absent header
// never inherits a parent's claim (FR-11.2a, MTIX-278).
package http

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// autoClaimServer builds a REST server whose node service has agent.auto_claim on
// and an in_progress parent claimed by "owner".
func autoClaimServer(t *testing.T) (*serverFixture, string) {
	t.Helper()
	dbDir := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	st, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	// Use a fixed sync identity so claim tests need no host network interfaces.
	_, seedErr := st.WriteDB().ExecContext(t.Context(), "UPDATE meta SET value = ? WHERE key = ?", "0123456789abcdef", "meta.sync.machine_hash")
	require.NoError(t, seedErr)
	bc := &service.NoopBroadcaster{}
	config := &service.StaticConfig{AutoClaimEnabled: true}
	clock := testClock()
	configSvc, err := service.NewConfigService("")
	require.NoError(t, err)
	nodes := service.NewNodeService(st, bc, config, logger, clock)
	background := service.NewBackgroundService(st, config, logger, clock)
	sessions := service.NewSessionService(st, config, logger, clock)
	agents := service.NewAgentService(st, bc, config, logger, clock)
	deps := service.NewDependencyServiceFromNodeService(nodes)
	admin := service.NewAdminService(st)
	services := Services{
		Read:  ReadServices{Nodes: nodes, Background: background, Sessions: sessions, Agents: agents, Config: configSvc, Dependencies: deps, Admin: admin},
		Write: WriteServices{Nodes: nodes, Background: background, Sessions: sessions, Agents: agents, Config: configSvc, Dependencies: deps, Admin: admin},
	}
	srv := &serverFixture{Server: NewServer(services, logger, ServerConfig{Bind: "127.0.0.1", Port: "0", RateLimit: 0}, clock), store: st}
	parent, err := nodes.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "TEST", Title: "Parent", Creator: "planner"})
	require.NoError(t, err)
	require.NoError(t, st.ClaimNode(context.Background(), parent.ID, "owner"))
	return srv, parent.ID
}

func postRESTAsAuthor(t *testing.T, s *serverFixture, path, body, author string) {
	t.Helper()
	req := newLocalRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	if author != "" {
		req.Header.Set("X-Agent-ID", author)
	}
	res := httptest.NewRecorder()
	s.Router().ServeHTTP(res, req)
	require.Equal(t, http.StatusCreated, res.Code, res.Body.String())
}

func TestRESTCreateAndDecompose_AuthorHeader_ClaimsOnlyForParentAssignee(t *testing.T) {
	tests := []struct {
		name         string
		author       string
		wantStatus   model.Status
		wantAssignee string
	}{
		{"missing header is open and unassigned", "", model.StatusOpen, ""},
		{"other agent is open and unassigned", "other", model.StatusOpen, ""},
		{"parent assignee inherits", "owner", model.StatusInProgress, "owner"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, parentID := autoClaimServer(t)
			postRESTAsAuthor(t, s, "/api/v1/nodes", `{"title":"Single","project":"TEST","parent_id":"`+parentID+`"}`, tt.author)
			postRESTAsAuthor(t, s, "/api/v1/nodes/"+parentID+"/decompose", `{"children":[{"title":"A"}]}`, tt.author)
			for _, id := range []string{parentID + ".1", parentID + ".2"} {
				got, err := s.store.GetNode(context.Background(), id)
				require.NoError(t, err)
				assert.Equal(t, tt.wantStatus, got.Status, id)
				assert.Equal(t, tt.wantAssignee, got.Assignee, id)
			}
		})
	}
}
