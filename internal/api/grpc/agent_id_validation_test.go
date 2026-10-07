// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Agent identity boundaries reject unsafe nonempty IDs without writes (MTIX-125).
package grpc

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func invalidAgentIDs() []struct{ name, id string } {
	return []struct{ name, id string }{
		{"spaces", "   "}, {"unicode_space", "\u2003\u00a0"},
		{"overlong", strings.Repeat("a", 65)}, {"utf8_bytes", strings.Repeat("é", 33)},
		{"nul", "a\x00b"}, {"tab", "a\tb"}, {"newline", "a\nb"},
		{"del", "a\x7fb"}, {"c1", "a\u0085b"},
		{"zero_width", "a\u200bb"}, {"joiner", "a\u200db"},
		{"bidi", "a\u202eb"}, {"bidi_isolate", "a\u2066b"},
		{"bom", "a\ufeffb"}, {"soft_hyphen", "a\u00adb"},
	}
}

func TestAgentID_GRPCBoundaries_RejectInvalidArgument(t *testing.T) {
	for _, op := range []string{"create", "claim", "reclaim", "unclaim"} {
		t.Run(op, func(t *testing.T) {
			for _, tc := range invalidAgentIDs() {
				t.Run(tc.name, func(t *testing.T) {
					s := testGRPCServer(t)
					ctx := context.Background()
					n, err := s.HandleCreateNode(ctx, &CreateNodeReq{Project: "TEST", Title: "existing"})
					require.NoError(t, err)
					if op == "reclaim" || op == "unclaim" {
						_, err = s.HandleClaim(ctx, n.ID, "old", false)
						require.NoError(t, err)
					}
					if op == "reclaim" {
						_, err = s.store.WriteDB().ExecContext(ctx, `UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`, "2000-01-01T00:00:00Z", "old")
						require.NoError(t, err)
					}
					before, err := s.store.GetNode(ctx, n.ID)
					require.NoError(t, err)
					switch op {
					case "create":
						_, err = s.HandleCreateNode(ctx, &CreateNodeReq{Project: "TEST", Title: "refused", Assignee: tc.id})
					case "unclaim":
						_, err = s.HandleUnclaim(ctx, n.ID, "release", tc.id)
					default:
						_, err = s.HandleClaim(ctx, n.ID, tc.id, op == "reclaim")
					}
					require.Equal(t, codes.InvalidArgument, status.Code(err))
					require.Contains(t, err.Error(), "agent ID")
					after, getErr := s.store.GetNode(ctx, n.ID)
					require.NoError(t, getErr)
					require.Equal(t, before, after)
				})
			}
		})
	}
}

func TestAgentID_GRPCValidRawAndEmptyCreate_Preserved(t *testing.T) {
	for _, agent := range []string{"", " Agent.User+é ", strings.Repeat("a", 64), strings.Repeat("é", 32)} {
		t.Run(agent, func(t *testing.T) {
			s := testGRPCServer(t)
			n, err := s.HandleCreateNode(context.Background(), &CreateNodeReq{Project: "TEST", Title: "raw", Assignee: agent})
			require.NoError(t, err)
			require.Equal(t, agent, n.Assignee)
			if agent != "" {
				_, err = s.HandleUnclaim(context.Background(), n.ID, "release", "actor")
				require.NoError(t, err)
				got, err := s.HandleClaim(context.Background(), n.ID, agent, false)
				require.NoError(t, err)
				require.Equal(t, agent, got.Assignee)
			}
		})
	}
}
