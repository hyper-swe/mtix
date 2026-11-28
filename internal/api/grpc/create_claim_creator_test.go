// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// gRPC create and decompose forward the creator field; an empty creator never
// inherits a parent's claim (FR-11.2a, MTIX-278).
package grpc

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// autoClaimGRPCServer builds a gRPC server whose node service has agent.auto_claim
// on and an in_progress parent claimed by "owner".
func autoClaimGRPCServer(t *testing.T) (*testServer, string) {
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
	srv := &testServer{store: st, Server: NewServer(Services{NodeSvc: nodes, BgSvc: service.NewBackgroundService(st, config, logger, clock),
		SessionSvc: service.NewSessionService(st, config, logger, clock), AgentSvc: service.NewAgentService(st, bc, config, logger, clock),
		ConfigSvc: configSvc, ContextSvc: service.NewContextService(st, config, logger), PromptSvc: service.NewPromptService(st, bc, logger, clock),
		Broadcaster: bc}, logger, ServerConfig{}, clock)}
	parent, err := nodes.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "TEST", Title: "Parent", Creator: "planner"})
	require.NoError(t, err)
	require.NoError(t, st.ClaimNode(context.Background(), parent.ID, "owner"))
	return srv, parent.ID
}

func TestGRPCCreateAndDecompose_Creator_ClaimsOnlyForParentAssignee(t *testing.T) {
	tests := []struct {
		name         string
		creator      string
		wantStatus   model.Status
		wantAssignee string
	}{
		{"missing creator is open and unassigned", "", model.StatusOpen, ""},
		{"other agent is open and unassigned", "other", model.StatusOpen, ""},
		{"parent assignee inherits", "owner", model.StatusInProgress, "owner"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, parentID := autoClaimGRPCServer(t)
			ctx := context.Background()
			_, err := s.HandleCreateNode(ctx, &CreateNodeReq{Title: "Single", Project: "TEST", ParentID: parentID, Creator: tt.creator})
			require.NoError(t, err)
			_, err = s.HandleDecompose(ctx, parentID, tt.creator, []DecomposeChildReq{{Title: "A"}})
			require.NoError(t, err)
			for _, id := range []string{parentID + ".1", parentID + ".2"} {
				got, err := s.store.GetNode(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, tt.wantStatus, got.Status, id)
				assert.Equal(t, tt.wantAssignee, got.Assignee, id)
			}
		})
	}
}
