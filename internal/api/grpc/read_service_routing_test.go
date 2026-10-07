// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/stretchr/testify/require"
	"testing"
)

type grpcReadBackend struct {
	store.Store
	calls map[string]int
}

func (s *grpcReadBackend) GetDirectChildren(context.Context, string) ([]*model.Node, error) {
	s.calls["children"]++
	return []*model.Node{{ID: "TEST-1.1"}}, nil
}
func (s *grpcReadBackend) ListNodes(context.Context, store.NodeFilter, store.ListOptions) ([]*model.Node, int, error) {
	s.calls["list"]++
	return []*model.Node{{ID: "TEST-1"}}, 1, nil
}
func (s *grpcReadBackend) GetBlockers(context.Context, string) ([]*model.Dependency, error) {
	s.calls["blockers"]++
	return []*model.Dependency{{FromID: "TEST-1", ToID: "TEST-2"}}, nil
}

func TestGRPCReads_UseOwningServiceBackend(t *testing.T) {
	for _, operation := range []string{"children", "list", "blockers"} {
		t.Run(operation, func(t *testing.T) {
			server := testGRPCServer(t)
			backend := &grpcReadBackend{Store: server.store, calls: map[string]int{}}
			server.nodeSvc = service.NewNodeService(backend, nil, nil, server.logger, server.clock)
			server.depSvc = service.NewDependencyServiceFromNodeService(server.nodeSvc)
			switch operation {
			case "children":
				_, _, err := server.HandleListChildren(t.Context(), "TEST-1", 5, 0)
				require.NoError(t, err)
			case "list":
				_, _, _, err := server.HandleSearch(t.Context(), service.NodeFilter{}, 5, 0)
				require.NoError(t, err)
			case "blockers":
				_, err := server.HandleGetDependencies(t.Context(), "TEST-1")
				require.NoError(t, err)
			}
			require.Equal(t, 1, backend.calls[operation], "handler must use actual owning service")
		})
	}
}
