// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// FR-19.1: recover's real stale-mirror plus newer-journal path must not swallow a later pull.
func TestRestoredInbox_RecoveredStaleMirrorAppliesRealComment(t *testing.T) {
	source := newTestStore(t)
	now := time.Now().UTC()
	require.NoError(t, source.CreateNode(t.Context(), makeRootNode("PROJ-1", "PROJ", "stale", now)))
	stale, err := source.Export(t.Context(), "", "")
	require.NoError(t, err)
	data, err := json.Marshal(stale)
	require.NoError(t, err)
	mirror := filepath.Join(t.TempDir(), "tasks.json")
	require.NoError(t, os.WriteFile(mirror, data, 0600))
	require.NoError(t, source.AppendAnnotation(t.Context(), "PROJ-1", model.Annotation{ID: "new-comment", Author: "sender", Text: "newer mail", Addressee: "recipient", CreatedAt: now}))
	recovered, err := sqlite.Recover(t.Context(), filepath.Join(t.TempDir(), "missing.db"), mirror, "test", sqlite.RecoverOptions{Logger: slog.Default(), InboxSources: []*sqlite.ExportData{transportExport(t, source)}})
	require.NoError(t, err)
	require.Empty(t, recovered.Export.Nodes[0].Annotations)
	require.Len(t, recovered.Export.InboxJournal.Events, 1)
	dest := newTestStore(t)
	_, err = dest.Import(t.Context(), recovered.Export, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	events, err := source.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	for _, event := range events {
		if event.OpType == model.OpComment {
			require.NoError(t, dest.WithTx(t.Context(), func(tx *sql.Tx) error { return dest.IdempotentApply(t.Context(), tx, event) }))
		}
	}
	node, err := dest.GetNode(t.Context(), "PROJ-1")
	require.NoError(t, err)
	require.Len(t, node.Annotations, 1)
	require.Equal(t, "newer mail", node.Annotations[0].Text)
	mail, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, mail, 1)
}
