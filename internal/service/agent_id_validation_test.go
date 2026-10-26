// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Agent identity boundaries reject unsafe nonempty IDs without writes (MTIX-125).
package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
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

func TestAgentID_ServiceBoundaries_RejectWithoutMutation(t *testing.T) {
	for _, op := range []string{"create", "claim", "reclaim", "unclaim", "update", "apply_update"} {
		t.Run(op, func(t *testing.T) {
			for _, tc := range invalidAgentIDs() {
				t.Run(tc.name, func(t *testing.T) {
					svc, st, bc := newTestNodeService(t)
					ctx := context.Background()
					req := &service.CreateNodeRequest{Project: "TEST", Title: "existing", Creator: "author"}
					if op == "unclaim" || op == "reclaim" {
						req.Assignee = "old"
					}
					n, err := svc.CreateNode(ctx, req)
					require.NoError(t, err)
					if op == "reclaim" {
						_, err = st.WriteDB().ExecContext(ctx, `UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`, "2000-01-01T00:00:00Z", "old")
						require.NoError(t, err)
					}
					before, err := st.GetNode(ctx, n.ID)
					require.NoError(t, err)
					events := len(bc.events)
					var countsBefore, countsAfter string
					query := `SELECT (SELECT COUNT(*) FROM nodes) || ',' || (SELECT COUNT(*) FROM agents) || ',' || (SELECT COUNT(*) FROM sync_events)`
					require.NoError(t, st.QueryRow(ctx, query).Scan(&countsBefore))
					err = agentIDServiceOperation(ctx, svc, op, n.ID, tc.id)
					require.ErrorIs(t, err, model.ErrInvalidInput)
					require.Contains(t, err.Error(), "agent ID")
					after, getErr := st.GetNode(ctx, n.ID)
					require.NoError(t, getErr)
					require.Equal(t, before, after)
					require.Len(t, bc.events, events)
					require.NoError(t, st.QueryRow(ctx, query).Scan(&countsAfter))
					require.Equal(t, countsBefore, countsAfter)
					if op == "create" {
						accepted, createErr := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "next", ParentID: n.ID})
						require.NoError(t, createErr)
						require.Equal(t, 1, accepted.Seq)
					}
				})
			}
		})
	}
}

func agentIDServiceOperation(ctx context.Context, svc *service.NodeService, op, id, agent string) error {
	switch op {
	case "create":
		_, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", ParentID: id, Title: "refused", Assignee: agent})
		return err
	case "claim":
		return svc.ClaimNode(ctx, id, agent)
	case "reclaim":
		return svc.ForceReclaimNode(ctx, id, agent, time.Hour)
	case "unclaim":
		return svc.UnclaimNode(ctx, id, "release", agent)
	case "apply_update":
		return svc.ApplyUpdate(ctx, id, &service.NodeUpdate{Assignee: &agent})
	default:
		return svc.UpdateNode(ctx, id, &store.NodeUpdate{Assignee: &agent})
	}
}

func TestAgentID_ServiceValidRawAndEmptyAssignment_Preserved(t *testing.T) {
	for _, agent := range []string{"", " Agent.User+é ", strings.Repeat("a", 64), strings.Repeat("é", 32)} {
		t.Run(agent, func(t *testing.T) {
			svc, st, _ := newTestNodeService(t)
			ctx := context.Background()
			n, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "raw", Assignee: agent})
			require.NoError(t, err)
			require.Equal(t, agent, n.Assignee)
			cleared := ""
			require.NoError(t, svc.UpdateNode(ctx, n.ID, &store.NodeUpdate{Assignee: &cleared}))
			got, err := st.GetNode(ctx, n.ID)
			require.NoError(t, err)
			require.Empty(t, got.Assignee)
		})
	}
}

func TestAgentID_ServiceValidWorkflowRaw_Preserved(t *testing.T) {
	for _, op := range []string{"claim", "reclaim", "unclaim", "update", "apply_update"} {
		t.Run(op, func(t *testing.T) {
			for _, agent := range []string{" Agent.User+é ", strings.Repeat("a", 64), strings.Repeat("é", 32)} {
				t.Run(agent, func(t *testing.T) {
					svc, st, bc := newTestNodeService(t)
					ctx := context.Background()
					req := &service.CreateNodeRequest{Project: "TEST", Title: "existing"}
					if op == "reclaim" || op == "unclaim" {
						req.Assignee = "old"
					}
					n, err := svc.CreateNode(ctx, req)
					require.NoError(t, err)
					if op == "reclaim" {
						_, err = st.WriteDB().ExecContext(ctx, `UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`, "2000-01-01T00:00:00Z", "old")
						require.NoError(t, err)
					}
					bc.Reset()
					require.NoError(t, agentIDServiceOperation(ctx, svc, op, n.ID, agent))
					got, err := st.GetNode(ctx, n.ID)
					require.NoError(t, err)
					if op == "unclaim" {
						require.Empty(t, got.Assignee)
						require.Equal(t, agent, bc.Events()[0].Author)
					} else {
						require.Equal(t, agent, got.Assignee)
					}
				})
			}
		})
	}
}
