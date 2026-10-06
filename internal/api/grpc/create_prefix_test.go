// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Local create boundaries share FR-2.1a prefix validation (MTIX-107.3).
package grpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestCreateRPC_PrefixGrammar_MapsSharedError(t *testing.T) {
	for _, prefix := range []string{"TEST_BAD", "test", "", "1TEST", "ABCDEFGHIJKLMNOPQRSTU", "TEST%", "A", "TEST-DEV-OPS", "ABCDEFGHIJKLMNOPQRST"} {
		t.Run(prefix, func(t *testing.T) {
			s := testGRPCServer(t)
			n, err := s.HandleCreateNode(t.Context(), &CreateNodeReq{Title: "Prefix boundary", Project: prefix})
			if expected := model.ValidatePrefix(prefix); expected != nil {
				require.Equal(t, codes.InvalidArgument, status.Code(err))
				assert.Equal(t, expected.Error(), status.Convert(err).Message())
				assert.Nil(t, n)
				projects, err := s.store.DistinctProjects(t.Context())
				require.NoError(t, err)
				assert.Empty(t, projects)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, prefix, n.Project)
			assert.Equal(t, prefix+"-1", n.ID)
		})
	}
}
