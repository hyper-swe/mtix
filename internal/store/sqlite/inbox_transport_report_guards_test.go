// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func insertGuardMail(t *testing.T, s *Store, author string) {
	t.Helper()
	_, err := s.WriteDB().Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,sync_status,created_at) VALUES('guard-mail','TEST','TEST-1','comment','{"to":"worker","body":"private guard mail"}',0,0,'{}',?,'','applied','2026-11-05T00:00:00Z')`, author)
	require.NoError(t, err)
}

func TestSalvageInbox_ReportIncompleteForMailOrAckLoss(t *testing.T) {
	for _, reason := range []string{"mail-loss", "ack-loss"} {
		t.Run(reason, func(t *testing.T) {
			s := exchangeSnapshotStore(t)
			author := "sender"
			if reason == "mail-loss" {
				author = strings.Repeat("s", 65)
			}
			insertGuardMail(t, s, author)
			if reason == "ack-loss" {
				_, err := s.WriteDB().Exec(`DROP TABLE agent_inbox_ack`)
				require.NoError(t, err)
			}
			report := &RecoverResult{}
			journal, err := salvageInboxRows(t.Context(), s.readDB, report)
			require.NoError(t, err, "row reads succeed; incomplete is caused by loss or ack coverage")
			require.True(t, report.InboxIncomplete)
			notes := strings.Join(report.Notes, " ")
			if reason == "mail-loss" {
				require.Empty(t, journal.Events)
				require.Contains(t, notes, "64-byte")
			} else {
				require.Len(t, journal.Events, 1)
				require.False(t, journal.Events[0].Acked)
				require.Contains(t, notes, "acknowledgment coverage unreadable")
			}
		})
	}
}

func TestDiscardLocal_RestoredMailArrivesAfterFirstGuard_RechecksTransaction(t *testing.T) {
	s := exchangeSnapshotStore(t)
	require.NoError(t, s.CreateNode(t.Context(), &model.Node{ID: "TEST-1", Project: "TEST", Seq: 1, Title: "keep", Status: model.StatusOpen, Priority: 3, Weight: 1}))
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.Mkdir(dir, 0700))
	arrived := false
	s.SetClock(func() time.Time {
		if !arrived {
			arrived = true
			insertGuardMail(t, s, "sender")
			_, err := s.WriteDB().Exec(`INSERT INTO inbox_restored_events VALUES('guard-mail')`)
			require.NoError(t, err)
		}
		return time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	})
	err := DiscardLocal(t.Context(), s, dir)
	require.True(t, arrived, "clock runs after the first guard and before the write transaction")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.ErrorContains(t, err, "restored mail")
	_, err = s.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	var count int
	require.NoError(t, s.QueryRow(t.Context(), `SELECT COUNT(*) FROM inbox_restored_events`).Scan(&count))
	require.Equal(t, 1, count)
	mail, err := s.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, mail, 1)
}

func TestImport_StoreMixedReplaceRefusal_UsesRestoredMailGuide(t *testing.T) {
	s := exchangeSnapshotStore(t)
	require.NoError(t, s.CreateNode(t.Context(), &model.Node{ID: "TEST-1", Project: "TEST", Seq: 1, Title: "keep", Status: model.StatusOpen, Priority: 3, Weight: 1}))
	insertGuardMail(t, s, "sender")
	_, err := s.WriteDB().Exec(`INSERT INTO inbox_restored_events VALUES('guard-mail')`)
	require.NoError(t, err)
	_, err = s.MarkHubConnected(t.Context(), "foreign_event")
	require.NoError(t, err)
	other := exchangeSnapshotStore(t)
	require.NoError(t, other.CreateNode(t.Context(), &model.Node{ID: "TEST-2", Project: "TEST", Seq: 2, Title: "other", Status: model.StatusOpen, Priority: 3, Weight: 1}))
	data, err := other.Export(t.Context(), "", "")
	require.NoError(t, err)
	_, err = s.Import(t.Context(), data, ImportModeReplace, false)
	require.ErrorIs(t, err, ErrReplaceOnSyncedStore)
	require.ErrorContains(t, err, "restored mail")
	require.ErrorContains(t, err, "new empty directory")
	require.NotContains(t, err.Error(), "--discard-local")
	mail, err := s.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, mail, 1)
	_, err = s.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
}
