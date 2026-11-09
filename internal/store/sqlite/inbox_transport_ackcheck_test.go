// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func openAckFixture(t *testing.T, flip bool) *sqlite.Store {
	t.Helper()
	path, _, _ := damageMailFixture(t, "")
	root, size := setDamageCoverage(t, path, 0, 251)
	if flip {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		flipAckIndexKey(t, data, root, size, 251, 250)
		require.NoError(t, os.WriteFile(path, data, 0600))
	}
	s, err := sqlite.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// FR-19.1 / MTIX-112.49: a flipped ack index key must not yield a wrong Acked value.
func TestExportWithInbox_FlippedAckIndexKey_RefusesNamingDoctor(t *testing.T) {
	s := openAckFixture(t, true)
	data, err := s.ExportWithInbox(t.Context(), "", "")
	require.Error(t, err)
	require.Nil(t, data)
	require.ErrorIs(t, err, model.ErrConflict)
	require.Contains(t, err.Error(), "mtix doctor")
}

// FR-19.1 / MTIX-112.49: a clean store exports the same coverage as before.
func TestExportWithInbox_CleanAckIndex_ExportsExactCoverage(t *testing.T) {
	s := openAckFixture(t, false)
	data, err := s.ExportWithInbox(t.Context(), "", "")
	require.NoError(t, err)
	require.NoError(t, sqlite.ValidateExport(data))
	require.Len(t, data.InboxJournal.Events, 300)
	acked := 0
	for _, e := range data.InboxJournal.Events {
		if e.EventID == "damage-251" {
			require.True(t, e.Acked)
			require.Equal(t, "2026-11-05T00:00:00Z", e.AckedAt)
			acked++
			continue
		}
		require.False(t, e.Acked, e.EventID)
		require.Empty(t, e.AckedAt, e.EventID)
	}
	require.Equal(t, 1, acked)
	_, err = json.Marshal(data)
	require.NoError(t, err)
}

// FR-19.1 / MTIX-112.49: the legacy plain reader stays unchanged and journal-free.
func TestExport_FlippedAckIndexKey_LegacyReaderUnchanged(t *testing.T) {
	s := openAckFixture(t, true)
	data, err := s.Export(t.Context(), "", "")
	require.NoError(t, err)
	require.Nil(t, data.InboxJournal)
}

// FR-19.1 / MTIX-112.49: a store with no acknowledgments is not refused.
func TestExportWithInbox_NoAcks_ExportsAllUnread(t *testing.T) {
	path, _, _ := damageMailFixture(t, "")
	s, err := sqlite.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	data, err := s.ExportWithInbox(t.Context(), "", "")
	require.NoError(t, err)
	for _, e := range data.InboxJournal.Events {
		require.False(t, e.Acked)
	}
}
