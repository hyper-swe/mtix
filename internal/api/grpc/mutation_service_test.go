// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

type mutationEvents struct{ events []service.Event }

func (b *mutationEvents) Broadcast(_ context.Context, event service.Event) error {
	b.events = append(b.events, event)
	return nil
}

func mutationServer(t *testing.T) (*Server, *mutationEvents) {
	t.Helper()
	old := testGRPCServer(t)
	bus := &mutationEvents{}
	nodes := service.NewNodeService(old.store, bus, nil, old.logger, old.clock)
	s := NewServer(old.store, nodes, old.bgSvc, old.sessionSvc, old.agentSvc, old.configSvc, old.contextSvc, old.promptSvc, bus, old.logger, old.config, old.clock)
	return s, bus
}

func TestGRPCMutations_InvokeServicesAndBroadcast(t *testing.T) {
	cases := []struct {
		name  string
		event service.EventType
	}{
		{"claim", service.EventNodeClaimed}, {"force_claim", service.EventNodeClaimed},
		{"unclaim", service.EventNodeUnclaimed}, {"cancel", service.EventNodeCancelled},
		{"dep_add", service.EventDependencyAdded}, {"dep_remove", service.EventDependencyRemoved},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s, bus := mutationServer(t)
			ctx := context.Background()
			node := createTestNode(t, s, "Source", "SERVICE")
			target := createTestNode(t, s, "Target", "SERVICE")
			dep := &model.Dependency{FromID: node.ID, ToID: target.ID, DepType: model.DepTypeRelated, CreatedBy: "worker"}
			if tt.name == "unclaim" || tt.name == "force_claim" {
				require.NoError(t, s.store.ClaimNode(ctx, node.ID, "previous"))
			}
			if tt.name == "force_claim" {
				// Give the existing assignment an explicitly stale heartbeat; no sleeps.
				_, err := s.store.WriteDB().ExecContext(ctx, `UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`, s.clock().Add(-time.Hour).UTC().Format(time.RFC3339), "previous")
				require.NoError(t, err)
			}
			if tt.name == "dep_remove" {
				require.NoError(t, s.store.AddDependency(ctx, dep))
			}
			bus.events = nil
			var err error
			switch tt.name {
			case "claim":
				_, err = s.HandleClaim(ctx, node.ID, "worker", false)
			case "force_claim":
				_, err = s.HandleClaim(ctx, node.ID, "worker", true)
			case "unclaim":
				_, err = s.HandleUnclaim(ctx, node.ID, "release", "worker")
			case "cancel":
				_, err = s.HandleCancel(ctx, node.ID, "cancel", "worker")
			case "dep_add":
				err = s.HandleAddDependency(ctx, dep)
			case "dep_remove":
				err = s.HandleRemoveDependency(ctx, node.ID, target.ID, model.DepTypeRelated)
			}
			require.NoError(t, err)
			require.Len(t, bus.events, 1, "handler must invoke the owning service")
			require.Equal(t, tt.event, bus.events[0].Type)
			require.Equal(t, node.ID, bus.events[0].NodeID)
		})
	}
}

func TestGRPCMutations_FailedWritesPreserveMappingWithoutBroadcast(t *testing.T) {
	for _, name := range []string{"claim", "force_claim", "unclaim", "cancel", "dep_add", "dep_remove"} {
		t.Run(name, func(t *testing.T) {
			s, bus := mutationServer(t)
			ctx := context.Background()
			var err error
			switch name {
			case "claim":
				_, err = s.HandleClaim(ctx, "MISSING-1", "worker", false)
			case "force_claim":
				_, err = s.HandleClaim(ctx, "MISSING-1", "worker", true)
			case "unclaim":
				_, err = s.HandleUnclaim(ctx, "MISSING-1", "release", "worker")
			case "cancel":
				_, err = s.HandleCancel(ctx, "MISSING-1", "cancel", "worker")
			case "dep_add":
				err = s.HandleAddDependency(ctx, &model.Dependency{FromID: "MISSING-1", ToID: "MISSING-2", DepType: model.DepTypeRelated})
			case "dep_remove":
				err = s.HandleRemoveDependency(ctx, "MISSING-1", "MISSING-2", model.DepTypeRelated)
			}
			require.Error(t, err)
			want := codes.NotFound
			if name == "dep_add" {
				want = codes.Internal
			}
			require.Equal(t, want, status.Code(err))
			require.Empty(t, bus.events)
		})
	}
}

func TestGRPCServiceBoundary_PreservesExactErrorMessage(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()
	backend := s.store.CancelNode(ctx, "MISSING-1", "cancel", "worker", false)
	_, err := s.HandleCancel(ctx, "MISSING-1", "cancel", "worker")
	require.Error(t, backend)
	require.Equal(t, codes.NotFound, status.Code(err))
	require.Equal(t, backend.Error(), status.Convert(err).Message())
}
