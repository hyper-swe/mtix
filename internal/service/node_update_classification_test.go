// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Classification updates validate before writes, persist and export (MTIX-107.60).
package service_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestUpdateNode_IssueType_PartialValidatedAndExported(t *testing.T) {
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", "", "epic", "BUG", " bug"} {
		t.Run(kind, func(t *testing.T) {
			svc, st, _ := newTestNodeService(t)
			ctx := context.Background()
			n, err := svc.CreateNode(ctx, createClassifiedRequest(t, "feature", ""))
			require.NoError(t, err)
			raw, err := json.Marshal(map[string]string{"issue_type": kind, "title": "Updated title"})
			require.NoError(t, err)
			var update service.NodeUpdate
			require.NoError(t, json.Unmarshal(raw, &update))
			err = svc.ApplyUpdate(ctx, n.ID, &update)
			if kind == "epic" || kind == "BUG" || kind == " bug" {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				got, e := st.GetNode(ctx, n.ID)
				require.NoError(t, e)
				assert.Equal(t, n.Title, got.Title)
				assert.Equal(t, n.IssueType, got.IssueType)
				var count int
				require.NoError(t, st.QueryRow(ctx, `SELECT COUNT(*) FROM sync_events WHERE op_type = 'update_field'`).Scan(&count))
				assert.Zero(t, count)
				return
			}
			require.NoError(t, err)
			got, err := st.GetNode(ctx, n.ID)
			require.NoError(t, err)
			assert.Equal(t, model.IssueType(kind), got.IssueType)
			require.NoError(t, svc.ApplyUpdate(ctx, n.ID, &service.NodeUpdate{}))
			got, err = st.GetNode(ctx, n.ID)
			require.NoError(t, err)
			assert.Equal(t, model.IssueType(kind), got.IssueType)
			var value sql.NullString
			require.NoError(t, st.QueryRow(ctx, `SELECT issue_type FROM nodes WHERE id = ?`, n.ID).Scan(&value))
			assert.Equal(t, kind != "", value.Valid)
			var payload string
			require.NoError(t, st.QueryRow(ctx, `SELECT payload FROM sync_events WHERE op_type = 'update_field' AND json_extract(payload,'$.field_name') = 'issue_type'`).Scan(&payload))
			var event model.UpdateFieldPayload
			require.NoError(t, json.Unmarshal([]byte(payload), &event))
			assert.JSONEq(t, string(rawIssueType(t, kind)), string(event.NewValue))
			data, err := st.Export(ctx, "TEST", "test")
			require.NoError(t, err)
			wire, err := json.Marshal(data)
			require.NoError(t, err)
			decoded, err := sqlite.DecodeExportData(bytes.NewReader(wire))
			require.NoError(t, err)
			_, target, _ := newTestNodeService(t)
			_, err = target.Import(ctx, decoded, sqlite.ImportModeReplace, true)
			require.NoError(t, err)
			restored, err := target.GetNode(ctx, n.ID)
			require.NoError(t, err)
			assert.Equal(t, model.IssueType(kind), restored.IssueType)
		})
	}
}

func rawIssueType(t *testing.T, kind string) []byte {
	t.Helper()
	raw, err := json.Marshal(kind)
	require.NoError(t, err)
	return raw
}

// Replay the exact locally emitted event stream, including clear, on another store.
func TestUpdateNode_IssueType_EmittedSyncRoundTrip(t *testing.T) {
	svc, source, _ := newTestNodeService(t)
	ctx := context.Background()
	n, err := svc.CreateNode(ctx, createClassifiedRequest(t, "feature", ""))
	require.NoError(t, err)
	_, target, _ := newTestNodeService(t)
	for _, kind := range []string{"bug", "", "refactor"} {
		value := model.IssueType(kind)
		require.NoError(t, svc.ApplyUpdate(ctx, n.ID, &service.NodeUpdate{IssueType: &value}))
		events, err := source.ReadPendingEvents(ctx, 100)
		require.NoError(t, err)
		for _, event := range events {
			require.NoError(t, target.WithTx(ctx, func(tx *sql.Tx) error { return sqlite.IdempotentApply(ctx, tx, event) }))
		}
		got, err := target.GetNode(ctx, n.ID)
		require.NoError(t, err)
		assert.Equal(t, value, got.IssueType)
		assert.Equal(t, n.NodeType, got.NodeType)
		assert.Equal(t, n.ContentHash, got.ContentHash)
	}
}

func TestUpdateNode_IssueType_EventFailureRollsBack(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	ctx := context.Background()
	n, err := svc.CreateNode(ctx, createClassifiedRequest(t, "feature", ""))
	require.NoError(t, err)
	_, err = st.WriteDB().ExecContext(ctx, `CREATE TRIGGER reject_type_event BEFORE INSERT ON sync_events WHEN NEW.op_type = 'update_field' BEGIN SELECT RAISE(ABORT, 'event refused'); END`)
	require.NoError(t, err)
	value := model.IssueTypeBug
	require.Error(t, svc.ApplyUpdate(ctx, n.ID, &service.NodeUpdate{IssueType: &value}))
	got, err := st.GetNode(ctx, n.ID)
	require.NoError(t, err)
	assert.Equal(t, n.IssueType, got.IssueType)
	assert.Equal(t, n.UpdatedAt, got.UpdatedAt)
}
