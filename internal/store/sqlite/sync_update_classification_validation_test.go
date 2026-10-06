// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Classification payload validity precedes idempotency and LWW shortcuts (MTIX-107.60).
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestSyncUpdate_IssueType_InvalidBeforeShortcuts_NoWrites(t *testing.T) {
	payloads := []struct{ name, raw string }{
		{"unknown", `{"field_name":"issue_type","new_value":"invalid"}`},
		{"null", `{"field_name":"issue_type","new_value":null}`},
		{"number", `{"field_name":"issue_type","new_value":42}`},
		{"missing", `{"field_name":"issue_type"}`},
		{"malformed", `{"field_name":"issue_type","new_value":`},
	}
	for _, path := range []string{"winner", "loser", "held", "duplicate"} {
		for _, payload := range payloads {
			t.Run(path+"/"+payload.name, func(t *testing.T) {
				s, _ := applyTestStore(t)
				ctx := context.Background()
				create := makeApplyEvent(t, model.OpCreateNode, "TEST-1", "author", 1, map[string]any{"title": "Classified work", "issue_type": "feature"})
				create.ProjectPrefix = "TEST"
				require.NoError(t, applyOnce(t, s, create))
				prior := classificationUpdateEvent(t, 10, `"bug"`)
				require.NoError(t, applyOnce(t, s, prior))
				incoming := classificationUpdateEvent(t, 20, `"doc"`)
				switch path {
				case "loser":
					incoming.LamportClock = 2
				case "held":
					require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error { return mirrorIncomingEvent(ctx, tx, incoming) }))
				case "duplicate":
					incoming.EventID = prior.EventID
				}
				incoming.Payload = json.RawMessage(payload.raw)
				incoming.VectorClock = model.VectorClock{"unrelated": 99}
				before := classificationApplySnapshot(t, s)
				err := applyOnce(t, s, incoming)
				assert.ErrorIs(t, err, model.ErrInvalidInput)
				assert.Equal(t, before, classificationApplySnapshot(t, s), "invalid event must change no rows or clocks")
			})
		}
	}
}

func TestSyncUpdate_IssueType_ValidLoserAndDuplicateSemantics(t *testing.T) {
	s, _ := applyTestStore(t)
	ctx := context.Background()
	create := makeApplyEvent(t, model.OpCreateNode, "TEST-1", "author", 1, map[string]any{"title": "Classified work", "issue_type": "feature"})
	create.ProjectPrefix = "TEST"
	require.NoError(t, applyOnce(t, s, create))
	winner := classificationUpdateEvent(t, 10, `"bug"`)
	require.NoError(t, applyOnce(t, s, winner))
	loser := classificationUpdateEvent(t, 2, `"doc"`)
	loser.VectorClock = model.VectorClock{"unrelated": 99}
	require.NoError(t, applyOnce(t, s, loser))
	got, err := s.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	assert.Equal(t, model.IssueTypeBug, got.IssueType)
	var count int
	require.NoError(t, s.QueryRow(ctx, `SELECT COUNT(*) FROM sync_events WHERE event_id = ?`, loser.EventID).Scan(&count))
	assert.Equal(t, 1, count)
	require.NoError(t, s.QueryRow(ctx, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, loser.EventID).Scan(&count))
	assert.Equal(t, 1, count)
	require.NoError(t, s.QueryRow(ctx, `SELECT COUNT(*) FROM sync_conflicts WHERE event_id_loser = ?`, loser.EventID).Scan(&count))
	assert.Equal(t, 1, count)
	var vc string
	require.NoError(t, s.QueryRow(ctx, `SELECT value FROM meta WHERE key = 'meta.sync.vector_clock'`).Scan(&vc))
	assert.Contains(t, vc, `"unrelated":99`)
	before := classificationApplySnapshot(t, s)
	require.NoError(t, applyOnce(t, s, loser))
	require.NoError(t, applyOnce(t, s, winner))
	assert.Equal(t, before, classificationApplySnapshot(t, s))
}

func classificationUpdateEvent(t *testing.T, lamport int64, value string) *model.SyncEvent {
	t.Helper()
	event := makeApplyEvent(t, model.OpUpdateField, "TEST-1", "author", lamport, &model.UpdateFieldPayload{FieldName: "issue_type", NewValue: json.RawMessage(value)})
	event.ProjectPrefix = "TEST"
	return event
}

// Snapshot every affected table, including metadata clocks, rather than just node classification.
func classificationApplySnapshot(t *testing.T, s *Store) map[string][][]any {
	t.Helper()
	tables := []struct{ name, query string }{
		{"nodes", `SELECT * FROM nodes ORDER BY id`},
		{"sync_events", `SELECT * FROM sync_events ORDER BY event_id`},
		{"applied_events", `SELECT * FROM applied_events ORDER BY event_id`},
		{"sync_conflicts", `SELECT * FROM sync_conflicts ORDER BY rowid`},
		{"meta", `SELECT * FROM meta ORDER BY key`},
	}
	state := make(map[string][][]any, len(tables))
	for _, table := range tables {
		state[table.name] = classificationSnapshotRows(t, s, table.query)
	}
	return state
}

func classificationSnapshotRows(t *testing.T, s *Store, query string) [][]any {
	t.Helper()
	rows, err := s.Query(context.Background(), query)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	columns, err := rows.Columns()
	require.NoError(t, err)
	result := make([][]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		require.NoError(t, rows.Scan(dest...))
		for i, value := range values {
			if raw, ok := value.([]byte); ok {
				values[i] = string(raw)
			}
		}
		result = append(result, values)
	}
	require.NoError(t, rows.Err())
	return result
}
