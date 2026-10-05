// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// Backfill must preserve every serialized create field and the durable UID through replay.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func backfillFieldsNode(id, parent string, issueType model.IssueType) *model.Node {
	now := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	depth := 0
	if parent != "" {
		depth = 1
	}
	return &model.Node{ID: id, ParentID: parent, Project: "TEST", Depth: depth, Seq: 1,
		Title: "Classified source", IssueType: issueType, NodeType: model.NodeTypeForDepth(depth),
		Description: "Source description", Prompt: "Source instructions", Acceptance: "Source acceptance",
		Priority: model.PriorityHigh, Labels: []string{"regression", "classification"},
		Assignee: "worker", Creator: "author", Status: model.StatusOpen, Weight: 1,
		CreatedAt: now, UpdatedAt: now}
}

func expectedBackfillPayload(n *model.Node) model.CreateNodePayload {
	return model.CreateNodePayload{Title: n.Title, ParentID: n.ParentID,
		IssueType: n.IssueType, NodeType: n.NodeType, Description: n.Description,
		Prompt: n.Prompt, Acceptance: n.Acceptance, Priority: n.Priority,
		Labels: n.Labels, Assignee: n.Assignee, Creator: n.Creator}
}

func TestBackfill_AllCreateFieldsAndUIDSurviveReplicaApply(t *testing.T) {
	types := append(model.AllIssueTypes(), model.IssueType(""))
	for _, issueType := range types {
		t.Run(string(issueType), func(t *testing.T) {
			source, replica := newTestStore(t), newTestStore(t)
			ctx := context.Background()
			require.NoError(t, source.CreateNode(ctx, backfillFieldsNode("TEST-1", "", "")))
			require.NoError(t, source.CreateNode(ctx, backfillFieldsNode("TEST-1.1", "TEST-1", issueType)))
			// This fresh fixture models a pre-sync store: retain nodes and their durable UIDs.
			_, err := source.WriteDB().ExecContext(ctx, "DELETE FROM sync_events")
			require.NoError(t, err)
			_, err = source.Backfill(ctx, false)
			require.NoError(t, err)
			events, err := source.ReadPendingEvents(ctx, 100)
			require.NoError(t, err)
			creates := 0
			for _, event := range events {
				if event.OpType == model.OpCreateNode {
					creates++
					original, readErr := source.GetNode(ctx, event.NodeID)
					require.NoError(t, readErr)
					var payload model.CreateNodePayload
					require.NoError(t, json.Unmarshal(event.Payload, &payload))
					assert.Equal(t, expectedBackfillPayload(original), payload, "all eleven serialized fields")
					require.NotEmpty(t, original.UID)
					assert.Equal(t, original.UID, event.UID)
				}
				require.NoError(t, replica.WithTx(ctx, func(tx *sql.Tx) error { return sqlite.IdempotentApply(ctx, tx, event) }))
			}
			assert.Equal(t, 2, creates)
			for _, id := range []string{"TEST-1", "TEST-1.1"} {
				original, readErr := source.GetNode(ctx, id)
				require.NoError(t, readErr)
				applied, readErr := replica.GetNode(ctx, id)
				require.NoError(t, readErr)
				assert.Equal(t, expectedBackfillPayload(original), expectedBackfillPayload(applied), "replica fields")
				assert.Equal(t, original.UID, applied.UID, "replica durable identity")
			}
		})
	}
}

func TestBackfill_NullAndEmptyLabelsRemainUnset(t *testing.T) {
	for _, labels := range []any{nil, "null", "[]"} {
		t.Run(stringLabelCase(labels), func(t *testing.T) {
			source, replica := newTestStore(t), newTestStore(t)
			ctx := context.Background()
			require.NoError(t, source.CreateNode(ctx, backfillFieldsNode("TEST-1", "", "")))
			// SQL NULL and JSON null/empty are valid empty source label representations.
			_, err := source.WriteDB().ExecContext(ctx, "UPDATE nodes SET labels = ?, issue_type = ? WHERE id = ?", labels, nil, "TEST-1")
			require.NoError(t, err)
			_, err = source.WriteDB().ExecContext(ctx, "DELETE FROM sync_events")
			require.NoError(t, err)
			_, err = source.Backfill(ctx, false)
			require.NoError(t, err)
			events, err := source.ReadPendingEvents(ctx, 100)
			require.NoError(t, err)
			var payload model.CreateNodePayload
			require.NoError(t, json.Unmarshal(events[0].Payload, &payload))
			assert.Empty(t, payload.Labels)
			assert.Empty(t, payload.IssueType)
			for _, event := range events {
				require.NoError(t, replica.WithTx(ctx, func(tx *sql.Tx) error { return sqlite.IdempotentApply(ctx, tx, event) }))
			}
			applied, err := replica.GetNode(ctx, "TEST-1")
			require.NoError(t, err)
			assert.Empty(t, applied.Labels)
			assert.Empty(t, applied.IssueType)
		})
	}
}

func stringLabelCase(labels any) string {
	if labels == nil {
		return "SQL_NULL"
	}
	return labels.(string)
}

func TestBackfill_MalformedLabelsRefusesAtomically(t *testing.T) {
	for _, labels := range []string{"[", "{}", "[1]"} {
		t.Run(labels, func(t *testing.T) {
			source := newTestStore(t)
			ctx := context.Background()
			require.NoError(t, source.CreateNode(ctx, backfillFieldsNode("TEST-1", "", model.IssueTypeBug)))
			require.NoError(t, source.CreateNode(ctx, backfillFieldsNode("TEST-2", "", model.IssueTypeTask)))
			// Corrupt only the isolated source fixture, after a valid row, to verify refusal.
			_, err := source.WriteDB().ExecContext(ctx, "UPDATE nodes SET labels = ? WHERE id = ?", labels, "TEST-2")
			require.NoError(t, err)
			_, err = source.WriteDB().ExecContext(ctx, "DELETE FROM sync_events")
			require.NoError(t, err)
			_, err = source.Backfill(ctx, false)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "labels")
			assert.Contains(t, err.Error(), "TEST-2")
			count, countErr := source.CountSyncEvents(ctx)
			require.NoError(t, countErr)
			assert.Zero(t, count)
			var syntaxErr *json.SyntaxError
			var typeErr *json.UnmarshalTypeError
			assert.True(t, errorsAsLabel(err, &syntaxErr, &typeErr), "JSON cause remains wrapped")
		})
	}
}

func errorsAsLabel(err error, syntaxErr **json.SyntaxError, typeErr **json.UnmarshalTypeError) bool {
	return errors.As(err, syntaxErr) || errors.As(err, typeErr)
}
