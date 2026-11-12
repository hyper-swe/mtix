// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// noticeFixture is a sync service over an empty store with its notices captured.
func noticeFixture(t *testing.T, synced bool) (*SyncService, *sqlite.Store, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	st, err := sqlite.New(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	if synced {
		_, err = st.MarkHubConnected(context.Background(), "config")
		require.NoError(t, err)
	}
	notices := &bytes.Buffer{}
	svc := NewSyncService(st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	svc.SetNoticeWriter(notices)
	return svc, st, notices
}

func noticeFor(svc *SyncService, diff *sqlite.ReplaceDiff) {
	svc.noticeImported(context.Background(), filepath.Join("proj", ".mtix"), diff, filepath.Join("proj", ".mtix", "b.db"), false)
}

// TestNoticeImported_AddedAndRemovedOnSyncedStore_StillSaysLocalOnly: an import
// that adds a task and also removes an unlogged local-only one still tells that
// the added task stays local only.
func TestNoticeImported_AddedAndRemovedOnSyncedStore_StillSaysLocalOnly(t *testing.T) {
	svc, _, notices := noticeFixture(t, true)
	noticeFor(svc, &sqlite.ReplaceDiff{Added: []string{"PROJ-9"}, Removed: []string{"PROJ-3"}})
	assert.Contains(t, notices.String(), "(PROJ-9) wrote no sync event and stay local only")
}

// TestNoticeImported_StoreSyncsUnreadable_StillPrintsNotice: when the store
// cannot say whether it syncs the notice is printed (saying too much is safe).
func TestNoticeImported_StoreSyncsUnreadable_StillPrintsNotice(t *testing.T) {
	svc, st, notices := noticeFixture(t, false)
	_, err := st.WriteDB().ExecContext(context.Background(), `DROP TABLE applied_events`)
	require.NoError(t, err)
	noticeFor(svc, &sqlite.ReplaceDiff{Added: []string{"PROJ-9"}})
	assert.Contains(t, notices.String(), "stay local only")
}

// TestNoticeImported_ManyAdded_ListsFiveAndCountsTheRest pins the bound.
func TestNoticeImported_ManyAdded_ListsFiveAndCountsTheRest(t *testing.T) {
	svc, _, notices := noticeFixture(t, true)
	noticeFor(svc, &sqlite.ReplaceDiff{Added: []string{"P-1", "P-2", "P-3", "P-4", "P-5", "P-6", "P-7"}})
	assert.Contains(t, notices.String(), "(P-1, P-2, P-3, P-4, P-5 and 2 more)")
	assert.NotContains(t, notices.String(), "P-6")

	notices.Reset()
	noticeFor(svc, &sqlite.ReplaceDiff{Added: []string{"P-1", "P-2", "P-3", "P-4", "P-5"}})
	assert.Contains(t, notices.String(), "(P-1, P-2, P-3, P-4, P-5)")
	assert.NotContains(t, notices.String(), "more")
}

// TestNoticeImported_SixAdded_ListsFiveAndCountsOne pins the bound at six.
func TestNoticeImported_SixAdded_ListsFiveAndCountsOne(t *testing.T) {
	svc, _, notices := noticeFixture(t, true)
	noticeFor(svc, &sqlite.ReplaceDiff{Added: []string{"P-1", "P-2", "P-3", "P-4", "P-5", "P-6"}})
	assert.Contains(t, notices.String(), "(P-1, P-2, P-3, P-4, P-5 and 1 more)")
	assert.NotContains(t, notices.String(), "P-6,")
	assert.NotContains(t, notices.String(), "P-5, P-6")
}

// TestNoticeImported_RemovalOnlyOnSyncedStore_NoLocalOnlyLine: an import that
// adds nothing never names an empty list of local-only tasks.
func TestNoticeImported_RemovalOnlyOnSyncedStore_NoLocalOnlyLine(t *testing.T) {
	svc, _, notices := noticeFixture(t, true)
	noticeFor(svc, &sqlite.ReplaceDiff{Removed: []string{"PROJ-3"}})
	assert.NotContains(t, notices.String(), "local only")
	assert.NotContains(t, notices.String(), "wrote no sync event")
	assert.NotContains(t, notices.String(), "()")
}
