// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"errors"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func requirePrintableStatus(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	msg := st.Message()
	require.True(t, utf8.ValidString(msg), "status text is valid UTF-8: %q", msg)
	for _, r := range msg {
		if format.SafeText(string(r)) != string(r) {
			t.Fatalf("unsafe rune %U in gRPC status text %q", r, msg)
		}
	}
}

func TestGRPCErrors_HostileIDs_NoControlBytes(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 8192 {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			s := testGRPCServer(t)
			ctx := context.Background()
			_, err := s.HandleGetNode(ctx, f.Value)
			requirePrintableStatus(t, err)
			_, err = s.HandleClaim(ctx, f.Value, "agent", false)
			requirePrintableStatus(t, err)
			_, err = s.HandleCreateNode(ctx, &CreateNodeReq{Title: "t", Project: "TEST", ParentID: f.Value, Creator: "author"})
			requirePrintableStatus(t, err)
		})
	}
}

func TestGRPCMapError_UnclassifiedHostileError_IsEscapedInternal(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 8192 {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			err := mapError(errors.New("boom " + f.Value))
			requirePrintableStatus(t, err)
			assert.Equal(t, codes.Internal, status.Code(err))
		})
	}
}
