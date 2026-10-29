// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Workflow conflict history preserves provenance while making a choice explicit.
package sqlite_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func workflowHistoryPair(t *testing.T, count int) (*historyFixture, *sqlite.ExportData, string) {
	t.Helper()
	local, raw := goldenStore(t)
	remote, _ := goldenStore(t)
	uid := goldenRootUID(t, raw)
	for _, f := range []*historyFixture{local, remote} {
		insertHistory(t, f.db, "01U0000000000000000000S001", uid, "assignee")
		f.s.SetHistoryExportSource(func() bool { return true })
	}
	_, err := local.db.Exec(`UPDATE nodes SET assignee = 'alice' WHERE uid = ?`, uid)
	require.NoError(t, err)
	_, err = remote.db.Exec(`UPDATE nodes SET assignee = 'bob' WHERE uid = ?`, uid)
	require.NoError(t, err)
	for i := 0; i < count; i++ {
		insertHistory(t, local.db, fmt.Sprintf("01U0000000000000000000L%03d", i), uid, "assignee")
		insertHistory(t, remote.db, fmt.Sprintf("01U0000000000000000000F%03d", i), uid, "assignee")
	}
	insertHistory(t, local.db, "01U0000000000000000000X001", uid, "priority")
	data, err := remote.s.Export(context.Background(), "PROJ", "0.6.0")
	require.NoError(t, err)
	return local, data, uid
}

func workflowHistoryJSON(t *testing.T, report *sqlite.ImportReconcileReport) map[string]any {
	t.Helper()
	raw, err := json.Marshal(report.WorkflowConflicts[0].Fields[0])
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func TestWorkflowHistory_ConflictListsBothSidesSinceSharedEntries(t *testing.T) {
	local, file, _ := workflowHistoryPair(t, 2)
	report, err := mergeWith(local.s, file, sqlite.WorkflowResolution{})
	require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
	require.Len(t, report.WorkflowConflicts, 1)
	field := workflowHistoryJSON(t, report)
	for _, tc := range []struct{ key, prefix string }{{"local_history", "L"}, {"file_history", "F"}} {
		rows, ok := field[tc.key].([]any)
		require.True(t, ok, "%s must be an array", tc.key)
		require.Len(t, rows, 2)
		assert.Equal(t, "01U0000000000000000000"+tc.prefix+"001", rows[0].(map[string]any)["id"])
		assert.Equal(t, "01U0000000000000000000"+tc.prefix+"000", rows[1].(map[string]any)["id"])
	}
	text := report.String()
	assert.Contains(t, text, "yours history")
	assert.Contains(t, text, "file history")
	assert.Contains(t, text, "01U0000000000000000000L001")
	assert.NotContains(t, text, "01U0000000000000000000S001", "shared base is omitted")
	assert.NotContains(t, text, "01U0000000000000000000X001", "unrelated field is omitted")
	var n int
	require.NoError(t, local.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE entry_id LIKE '01U%'`).Scan(&n))
	assert.Equal(t, 4, n, "refused planning writes no history")
}

func TestWorkflowHistory_ReportBoundsAndMissingHistory(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		omit  bool
	}{{"bounded", 9, false}, {"no peer history", 2, true}} {
		t.Run(tc.name, func(t *testing.T) {
			local, file, _ := workflowHistoryPair(t, tc.count)
			if tc.omit {
				file.Nodes[0].History = nil
				require.NoError(t, sqlite.RecomputeExportChecksum(file))
			}
			report, err := mergeWith(local.s, file, sqlite.WorkflowResolution{})
			require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
			field := workflowHistoryJSON(t, report)
			if tc.omit {
				assert.Empty(t, field["file_history"])
				assert.Contains(t, report.String(), "no entries supplied")
			} else {
				for _, key := range []string{"local_history", "file_history"} {
					rows, ok := field[key].([]any)
					require.True(t, ok)
					assert.Len(t, rows, 5)
				}
				assert.Equal(t, float64(4), field["local_history_more"])
				assert.Equal(t, float64(4), field["file_history_more"])
				assert.Contains(t, report.String(), "4 more")
			}
		})
	}
}

func TestWorkflowHistory_ResolutionKeepsUnionAndOneValueFreeReceipt(t *testing.T) {
	for _, choice := range []sqlite.WorkflowChoice{sqlite.WorkflowOurs, sqlite.WorkflowTheirs} {
		t.Run(string(choice), func(t *testing.T) {
			local, file, uid := workflowHistoryPair(t, 2)
			report, err := mergeWith(local.s, file, sqlite.WorkflowResolution{Prefer: choice})
			require.NoError(t, err)
			require.True(t, report.Applied)
			assert.Len(t, historyIDs(t, local.db), 6, "shared+two local+two peer+unrelated remain")
			var n int
			require.NoError(t, local.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = ? AND field = ? AND old_value IS NULL AND new_value IS NULL`, uid, "resolution:"+string(choice)+":assignee").Scan(&n))
			assert.Equal(t, 1, n, "one explicit resolution record even when ours changes nothing")
			// Repeating the now-agreeing choice with an empty preference creates no receipt.
			snapshot, err := local.s.Export(context.Background(), "PROJ", "0.6.0")
			require.NoError(t, err)
			_, err = mergeWith(local.s, snapshot, sqlite.WorkflowResolution{})
			require.NoError(t, err)
			require.NoError(t, local.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = ? AND field = ?`, uid, "resolution:"+string(choice)+":assignee").Scan(&n))
			assert.Equal(t, 1, n)
		})
	}
}

func TestWorkflowHistory_HostileActorIsQuotedAndJSONPreservesIt(t *testing.T) {
	local, file, _ := workflowHistoryPair(t, 1)
	actor := "worker\n\x1b[31m\u202e"
	_, err := local.db.Exec(`UPDATE task_history SET actor = ? WHERE entry_id = ?`, actor, "01U0000000000000000000L000")
	require.NoError(t, err)
	report, err := mergeWith(local.s, file, sqlite.WorkflowResolution{})
	require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
	field := workflowHistoryJSON(t, report)
	rows := field["local_history"].([]any)
	assert.Equal(t, actor, rows[0].(map[string]any)["actor"])
	text := report.String()
	assert.NotContains(t, text, actor)
	assert.NotContains(t, text, "\x1b")
	assert.NotContains(t, text, "\u202e")
	assert.Contains(t, text, `worker\n\x1b[31m\u202e`)
}

func TestWorkflowHistory_NewestTimestampPrecedesEntryID(t *testing.T) {
	local, file, _ := workflowHistoryPair(t, 2)
	_, err := local.db.Exec(`UPDATE task_history SET ts = '2026-03-14T12:00:01.000000000Z' WHERE entry_id = ?`, "01U0000000000000000000L000")
	require.NoError(t, err)
	report, err := mergeWith(local.s, file, sqlite.WorkflowResolution{})
	require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
	rows := workflowHistoryJSON(t, report)["local_history"].([]any)
	assert.Equal(t, "01U0000000000000000000L000", rows[0].(map[string]any)["id"])
}

func TestWorkflowHistory_ResolutionFailureRollsBackChoiceAndHistoryUnion(t *testing.T) {
	local, file, uid := workflowHistoryPair(t, 2)
	_, err := local.db.Exec(`CREATE TRIGGER reject_workflow_receipt BEFORE INSERT ON task_history
  WHEN NEW.kind = 'resolution' BEGIN SELECT RAISE(ABORT, 'receipt refused'); END`)
	require.NoError(t, err)
	before := historyIDs(t, local.db)
	_, err = mergeWith(local.s, file, sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs})
	require.ErrorContains(t, err, "receipt refused")
	assert.Equal(t, before, historyIDs(t, local.db))
	var assignee string
	require.NoError(t, local.db.QueryRow(`SELECT assignee FROM nodes WHERE uid = ?`, uid).Scan(&assignee))
	assert.Equal(t, "alice", assignee)
	var n int
	require.NoError(t, local.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE kind = 'resolution'`).Scan(&n))
	assert.Zero(t, n)
}

func TestWorkflowHistory_ResolutionReceiptIsAdministrativeAndValueFree(t *testing.T) {
	local, file, uid := workflowHistoryPair(t, 1)
	_, err := mergeWith(local.s, file, sqlite.WorkflowResolution{Prefer: sqlite.WorkflowOurs})
	require.NoError(t, err)
	var kind, actor, origin, stamp string
	require.NoError(t, local.db.QueryRow(`SELECT kind, actor, origin, ts FROM task_history WHERE task_uid = ? AND field = ?`, uid, "resolution:ours:assignee").Scan(&kind, &actor, &origin, &stamp))
	assert.Equal(t, "resolution", kind)
	assert.NotEmpty(t, actor)
	assert.Equal(t, "import", origin)
	assert.Equal(t, "2026-03-14T12:00:00.000000000Z", stamp)
	assert.False(t, strings.Contains(kind+actor+origin, "alice"))
}

func TestWorkflowHistory_LegacyFileResolutionUsesStoredUID(t *testing.T) {
	local, file, uid := workflowHistoryPair(t, 1)
	file.Nodes[0].UID = ""
	file.Nodes[0].History = nil
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	_, err := mergeWith(local.s, file, sqlite.WorkflowResolution{Prefer: sqlite.WorkflowOurs})
	require.NoError(t, err)
	var n int
	require.NoError(t, local.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = ? AND kind = 'resolution'`, uid).Scan(&n))
	assert.Equal(t, 1, n, "legacy input must not detach the decision from the durable local task")
}

func TestWorkflowHistory_UnreadableHistoryRefusesBeforeWriting(t *testing.T) {
	local, file, uid := workflowHistoryPair(t, 1)
	local.s.SetHistoryExportSource(func() bool { return false })
	_, err := local.db.Exec(`DROP TABLE task_history`)
	require.NoError(t, err)
	_, err = mergeWith(local.s, file, sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs})
	require.ErrorContains(t, err, "read workflow history")
	var assignee string
	require.NoError(t, local.db.QueryRow(`SELECT assignee FROM nodes WHERE uid = ?`, uid).Scan(&assignee))
	assert.Equal(t, "alice", assignee)
}
