// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Creation classification and explicit claims preserve FR-3/FR-10 and atomic sync writes.
package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func createClassifiedRequest(t *testing.T, issueType, assignee string) *service.CreateNodeRequest {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"project": "TEST", "title": "Classified work", "creator": "author", "issue_type": issueType, "assignee": assignee})
	require.NoError(t, err)
	var req service.CreateNodeRequest
	require.NoError(t, json.Unmarshal(raw, &req))
	return &req
}

func TestCreateNode_ClassificationAndAssignment(t *testing.T) {
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", ""} {
		t.Run(kind, func(t *testing.T) {
			svc, st, _ := newTestNodeService(t)
			node, err := svc.CreateNode(context.Background(), createClassifiedRequest(t, kind, "worker"))
			require.NoError(t, err)
			stored, err := st.GetNode(context.Background(), node.ID)
			require.NoError(t, err)
			for _, n := range []*model.Node{node, stored} {
				assert.Equal(t, model.IssueType(kind), n.IssueType)
				assert.Equal(t, model.NodeTypeEpic, n.NodeType)
				assert.Equal(t, "author", n.Creator)
				assert.Equal(t, "worker", n.Assignee)
				assert.Equal(t, model.StatusInProgress, n.Status)
				assert.Equal(t, model.AgentStateWorking, n.AgentState)
			}
			var activityRaw string
			require.NoError(t, st.QueryRow(context.Background(), `SELECT activity FROM nodes WHERE id = ?`, node.ID).Scan(&activityRaw))
			var entries []model.ActivityEntry
			require.NoError(t, json.Unmarshal([]byte(activityRaw), &entries))
			require.Len(t, entries, 2)
			assert.Equal(t, model.ActivityTypeCreated, entries[0].Type)
			assert.Equal(t, "author", entries[0].Author)
			assert.Equal(t, model.ActivityTypeClaim, entries[1].Type)
			assert.Equal(t, "worker", entries[1].Author)
			var current, state string
			require.NoError(t, st.QueryRow(context.Background(), `SELECT current_node_id,state FROM agents WHERE agent_id = ?`, "worker").Scan(&current, &state))
			assert.Equal(t, node.ID, current)
			assert.Equal(t, "working", state)
			var ops string
			require.NoError(t, st.QueryRow(context.Background(), `SELECT group_concat(op_type, ',') FROM (SELECT op_type FROM sync_events ORDER BY rowid)`).Scan(&ops))
			assert.Equal(t, "create_node,claim", ops)
		})
	}
}

func TestCreateNode_InvalidIssueType_DoesNotWrite(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	_, err := svc.CreateNode(context.Background(), createClassifiedRequest(t, "epic", "worker"))
	require.ErrorIs(t, err, model.ErrInvalidInput)
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc"} {
		assert.Contains(t, err.Error(), kind)
	}
	var count int
	require.NoError(t, st.QueryRow(context.Background(), `SELECT COUNT(*) FROM nodes`).Scan(&count))
	assert.Zero(t, count)
	require.NoError(t, st.QueryRow(context.Background(), `SELECT COUNT(*) FROM sequences`).Scan(&count))
	assert.Zero(t, count)
}

func TestCreateNode_ExplicitClaimFailure_RollsBack(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER reject_claim BEFORE INSERT ON sync_events WHEN NEW.op_type = 'claim' BEGIN SELECT RAISE(ABORT, 'claim event refused'); END`,
		`CREATE TRIGGER reject_agent BEFORE INSERT ON agents BEGIN SELECT RAISE(ABORT, 'agent refused'); END`,
	} {
		t.Run(trigger, func(t *testing.T) {
			svc, st, _ := newTestNodeService(t)
			_, err := st.WriteDB().ExecContext(context.Background(), trigger)
			require.NoError(t, err)
			_, err = svc.CreateNode(context.Background(), createClassifiedRequest(t, "bug", "worker"))
			require.Error(t, err)
			var count int
			for _, query := range []string{`SELECT COUNT(*) FROM nodes`, `SELECT COUNT(*) FROM agents`, `SELECT COUNT(*) FROM sync_events`} {
				require.NoError(t, st.QueryRow(context.Background(), query).Scan(&count))
				assert.Zero(t, count)
			}
		})
	}
}

func TestCreateNode_OmittedAssignment_RemainsOpen(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	n, err := svc.CreateNode(context.Background(), createClassifiedRequest(t, "", ""))
	require.NoError(t, err)
	assert.Empty(t, n.IssueType)
	assert.Empty(t, n.Assignee)
	assert.Equal(t, model.StatusOpen, n.Status)
	assert.Equal(t, "author", n.Creator)
}

func TestCreateNode_ExplicitAssignment_OverridesParentAutoClaim(t *testing.T) {
	for _, assignee := range []string{"worker", ""} {
		t.Run(assignee, func(t *testing.T) {
			st, err := sqlite.New(t.TempDir(), slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, st.Close()) })
			bc := newRecordingBroadcaster()
			svc := service.NewNodeService(st, bc, &service.StaticConfig{AutoClaimEnabled: true}, nil, fixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
			parent, err := svc.CreateNode(context.Background(), createClassifiedRequest(t, "feature", "parent-worker"))
			require.NoError(t, err)
			bc.Reset()
			req := createClassifiedRequest(t, "task", assignee)
			req.ParentID = parent.ID
			child, err := svc.CreateNode(context.Background(), req)
			require.NoError(t, err)
			got, err := st.GetNode(context.Background(), child.ID)
			require.NoError(t, err)
			want := assignee
			if want == "" {
				want = "parent-worker"
			}
			assert.Equal(t, want, got.Assignee)
			assert.Equal(t, "author", got.Creator)
			assert.Equal(t, model.StatusInProgress, got.Status)
			assert.Equal(t, model.NodeTypeStory, got.NodeType)
			assert.Equal(t, model.IssueTypeTask, got.IssueType)
			if assignee != "" {
				events := bc.Events()
				require.Len(t, events, 2)
				assert.Equal(t, service.EventNodeCreated, events[0].Type)
				assert.Equal(t, "author", events[0].Author)
				assert.Equal(t, service.EventNodeClaimed, events[1].Type)
				assert.Equal(t, "worker", events[1].Author)
			}
		})
	}
}

func TestCreateNode_ExplicitAssignment_ReplaysExistingClaimEvent(t *testing.T) {
	svc, source, _ := newTestNodeService(t)
	created, err := svc.CreateNode(context.Background(), createClassifiedRequest(t, "feature", "worker"))
	require.NoError(t, err)
	events, err := source.ReadPendingEvents(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, model.OpCreateNode, events[0].OpType)
	assert.Equal(t, "author", events[0].AuthorID)
	assert.Equal(t, model.OpClaim, events[1].OpType)
	assert.Equal(t, "worker", events[1].AuthorID)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(events[0].Payload, &fields))
	assert.Equal(t, "feature", fields["issue_type"])
	assert.NotContains(t, fields, "assignee", "assignment must be replayed by the normal claim op")
	_, target, _ := newTestNodeService(t)
	for _, event := range events {
		require.NoError(t, target.WithTx(context.Background(), func(tx *sql.Tx) error { return sqlite.IdempotentApply(context.Background(), tx, event) }))
	}
	got, err := target.GetNode(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, model.IssueTypeFeature, got.IssueType)
	assert.Equal(t, "worker", got.Assignee)
	assert.Equal(t, "author", got.Creator)
	assert.Equal(t, model.StatusInProgress, got.Status)
}
