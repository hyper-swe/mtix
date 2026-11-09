// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"encoding/binary"
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
func TestExportWithInbox_FlippedAckIndexKey_RefusesNamingRecover(t *testing.T) {
	s := openAckFixture(t, true)
	data, err := s.ExportWithInbox(t.Context(), "", "")
	require.Error(t, err)
	require.Nil(t, data)
	require.ErrorIs(t, err, model.ErrConflict)
	require.Contains(t, err.Error(), "mtix recover")
	require.NotContains(t, err.Error(), "mtix doctor")
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

// Open a store whose only ack row has an explicit rowid, then damage one field
// of its one-cell index leaf (agent key or rowid pointer), table untouched.
func openAckFieldFixture(t *testing.T, field string) *sqlite.Store {
	t.Helper()
	path, _, _ := damageMailFixture(t, "")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_inbox_ack(rowid,agent_id,event_seq,acked_at) VALUES(5,'recipient',251,'2026-11-05T00:00:00Z')`)
	require.NoError(t, err)
	var root, size int
	require.NoError(t, db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name='sqlite_autoindex_agent_inbox_ack_1'`).Scan(&root))
	require.NoError(t, db.QueryRow(`PRAGMA page_size`).Scan(&size))
	require.NoError(t, db.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	page := (root - 1) * size
	require.Equal(t, byte(10), data[page])
	require.Equal(t, uint16(1), binary.BigEndian.Uint16(data[page+3:]))
	cell := page + int(binary.BigEndian.Uint16(data[page+8:]))
	_, prefix := rowVarint(data[cell:])
	record := cell + prefix
	header, _ := rowVarint(data[record:])
	agentAt := record + int(header)
	rowidAt := agentAt + len("recipient") + 2 // two-byte seq key sits between
	switch field {
	case "agent":
		require.Equal(t, byte('t'), data[agentAt+len("recipient")-1])
		data[agentAt+len("recipient")-1] ^= 0x01
	case "rowid":
		require.Equal(t, byte(5), data[rowidAt])
		data[rowidAt] ^= 0x01
	}
	require.NoError(t, os.WriteFile(path, data, 0600))
	s, err := sqlite.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// FR-19.1 / MTIX-112.49: damage to the agent key or the rowid pointer refuses.
func TestExportWithInbox_AgentKeyOrRowidDamage_RefusesNamingRecover(t *testing.T) {
	for _, field := range []string{"agent", "rowid"} {
		t.Run(field, func(t *testing.T) {
			s := openAckFieldFixture(t, field)
			data, err := s.ExportWithInbox(t.Context(), "", "")
			require.Error(t, err)
			require.Nil(t, data)
			require.ErrorIs(t, err, model.ErrConflict)
			require.Contains(t, err.Error(), "mtix recover")
		})
	}
}

// FR-19.1 / MTIX-112.49: the same fixture undamaged exports with the ack time.
func TestExportWithInbox_ExplicitRowidAck_ExportsAckedAt(t *testing.T) {
	s := openAckFieldFixture(t, "none")
	data, err := s.ExportWithInbox(t.Context(), "", "")
	require.NoError(t, err)
	for _, e := range data.InboxJournal.Events {
		if e.EventID == "damage-251" {
			require.True(t, e.Acked)
			require.Equal(t, "2026-11-05T00:00:00Z", e.AckedAt)
		}
	}
}
