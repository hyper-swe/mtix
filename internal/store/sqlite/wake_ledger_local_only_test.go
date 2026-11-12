// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Pins that the wake ledger stays local and that the exactly-once rule needs no schema
// change: no column was added, an older store without the reason column still reads, and
// no export, exchange snapshot or sync source carries a ledger row (MTIX-112.40.13).
package sqlite_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func reclaimStore(t *testing.T) (*sqlite.Store, *time.Time) {
	t.Helper()
	s, err := sqlite.New(t.TempDir(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	return s, &now
}

func TestWakeLedgerStats_ReclaimedAndCapped_AreCounted(t *testing.T) {
	s, now := reclaimStore(t)
	key := model.WakeKey{AgentID: "a", HostID: "h", UID: "u", EventSeq: 1, Epoch: 1}
	_, _, err := s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	*now = now.Add(2 * time.Minute)
	_, _, err = s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.Reclaimed)
	require.Zero(t, stats.Capped)
	*now = now.Add(2 * time.Minute)
	_, _, err = s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	stats, err = s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Zero(t, stats.Reclaimed)
	require.EqualValues(t, 1, stats.Capped)
}

func TestWakeLedgerStats_StoreWithoutReasonColumn_ReadsZeroReclaimsAndWritesNothing(t *testing.T) {
	s, _ := reclaimStore(t)
	_, err := s.WriteDB().ExecContext(t.Context(), "INSERT INTO wake_ledger(agent_id,host_id,uid,event_seq,epoch,claimed_at,outcome,attempt_token) VALUES('a','h','u',1,0,'2000-01-01T00:00:00Z','delivered','t')")
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(t.Context(), "ALTER TABLE wake_ledger DROP COLUMN reason")
	require.NoError(t, err, "simulate a store written before the reason column existed")
	var before, after int64
	require.NoError(t, s.WriteDB().QueryRow("SELECT total_changes()").Scan(&before))
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.Rows)
	require.Zero(t, stats.Reclaimed+stats.Capped)
	require.NoError(t, s.WriteDB().QueryRow("SELECT total_changes()").Scan(&after))
	require.Equal(t, before, after, "the read must not write")
}

func TestWakeClaim_ExactlyOnceRule_AddsNoLedgerColumn(t *testing.T) {
	s, _ := reclaimStore(t)
	rows, err := s.ReadDB().QueryContext(t.Context(), "SELECT name FROM pragma_table_info('wake_ledger') ORDER BY cid")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var got []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		got = append(got, n)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"agent_id", "host_id", "uid", "event_seq", "epoch", "claimed_at", "outcome", "attempt_token", "route", "reason", "outcome_at", "charged"}, got,
		"the retry count lives in the reason column; a new column would need its own upgrade test")
}

func TestWakeLedger_StaysLocal_NeverInExportOrSyncSources(t *testing.T) {
	s, _ := reclaimStore(t)
	at, won, err := s.ClaimWake(t.Context(), model.WakeKey{AgentID: "a", HostID: "h", UID: "u", EventSeq: 1, Epoch: 1}, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	for name, run := range map[string]func() (any, error){
		"export": func() (any, error) { return s.Export(t.Context(), "TEST", "test") },
		"exchange": func() (any, error) {
			d, _, e := s.ExportExchangeSnapshot(t.Context(), "TEST", "test", nil, 1<<20)
			return d, e
		},
	} {
		data, err := run()
		require.NoError(t, err, name)
		raw, err := json.Marshal(data)
		require.NoError(t, err)
		require.NotContains(t, string(raw), at.Token, "%s carries a wake fence", name)
		require.NotContains(t, strings.ToLower(string(raw)), "wake_ledger", name)
	}
	for _, dir := range []string{".", filepath.Join("..", "..", "sync")} {
		require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			require.NoError(t, err)
			name := d.Name()
			isExportOrSync := strings.HasPrefix(name, "export") || strings.HasPrefix(name, "sync_") || dir != "."
			if d.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || !isExportOrSync {
				return nil
			}
			b, rerr := os.ReadFile(p)
			require.NoError(t, rerr)
			require.NotContains(t, string(b), "wake_ledger", "%s must not read or write the local wake ledger", p)
			return nil
		}))
	}
}
