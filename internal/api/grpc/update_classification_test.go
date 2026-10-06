// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// gRPC classification update DTO preserves omission and explicit clears.
package grpc

import (
	"context"
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestUpdateRPC_IssueType_PartialValidated(t *testing.T) {
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", "", "invalid", "omitted"} {
		t.Run(kind, func(t *testing.T) {
			s := testGRPCServer(t)
			ctx := context.Background()
			n, err := s.HandleCreateNode(ctx, &CreateNodeReq{Title: "Classified work", Project: "TEST", IssueType: model.IssueTypeFeature})
			require.NoError(t, err)
			fields := map[string]string{"id": n.ID, "title": "Updated title"}
			if kind != "omitted" {
				fields["issue_type"] = kind
			}
			raw, err := json.Marshal(fields)
			require.NoError(t, err)
			var req UpdateNodeReq
			require.NoError(t, json.Unmarshal(raw, &req))
			_, err = s.HandleUpdateNode(ctx, &req)
			if kind == "invalid" {
				require.Equal(t, codes.InvalidArgument, status.Code(err))
			} else {
				require.NoError(t, err)
			}
			got, err := s.store.GetNode(ctx, n.ID)
			require.NoError(t, err)
			want := kind
			if kind == "omitted" || kind == "invalid" {
				want = "feature"
			}
			assert.Equal(t, model.IssueType(want), got.IssueType)
			if kind == "invalid" {
				assert.Equal(t, n.Title, got.Title)
			}
		})
	}
}
