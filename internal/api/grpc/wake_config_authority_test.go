// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
	rpcgrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestWakeConfig_GRPCRefusesMutationAndUnscopedAuthority(t *testing.T) {
	testutil.OperatorState(t)
	s := testGRPCServer(t)
	listener := bufconn.Listen(1 << 20)
	joined := make(chan error, 1)
	go func() { joined <- s.GRPCServer().Serve(listener) }()
	t.Cleanup(func() { s.Stop(); require.NoError(t, <-joined) })
	client, err := rpcgrpc.NewClient("passthrough:///wake-policy", rpcgrpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }), rpcgrpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// No config mutation RPC is exposed. Keep that transport boundary explicit.
	for _, method := range []string{"SetConfig", "UpdateConfig"} {
		err = client.Invoke(ctx, "/mtix.v1.MtixService/"+method, wrapperspb.String(`{"wake.max_hop_depth":"4096","wake.live":"on"}`), &emptypb.Empty{})
		require.Equal(t, codes.Unimplemented, status.Code(err))
	}
	// Even a future adapter cannot borrow the server stdin through this service.
	_, err = s.configSvc.Set("wake.max_hop_depth", "4096")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	_, err = s.configSvc.Set("wake.live", "off")
	require.ErrorIs(t, err, model.ErrInvalidInput)
}
