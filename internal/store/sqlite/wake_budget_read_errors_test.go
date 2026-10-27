// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeBudgetStore_CorruptOrMissingCounters_AreRefused(t *testing.T) {
	for _, value := range []any{int64(-1), "not a counter", nil} {
		t.Run(modelValueName(value), func(t *testing.T) {
			s, key := budgetDataStore(t)
			_, err := s.WriteDB().ExecContext(t.Context(), "DROP TABLE wake_budget_counters")
			require.NoError(t, err)
			// Model an externally damaged/replaced table, bypassing the normal CHECK constraint.
			_, err = s.WriteDB().ExecContext(t.Context(), "CREATE TABLE wake_budget_counters(agent_id TEXT,host_id TEXT,uid TEXT,total,PRIMARY KEY(agent_id,host_id,uid))")
			require.NoError(t, err)
			_, err = s.WriteDB().ExecContext(t.Context(), "INSERT INTO wake_budget_counters(agent_id,host_id,uid,total) VALUES(?,?,?,?)", key.AgentID, key.HostID, key.UID, value)
			require.NoError(t, err)
			_, err = s.WakeBudgetCounters(t.Context())
			require.Error(t, err)
			_, err = s.ClaimWakeBudget(t.Context(), key, time.Minute, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
				t.Error("corrupt counters must be refused before admission")
				return model.WakeBudgetDecision{Allow: true}, nil
			})
			require.Error(t, err)
		})
	}
	s, _ := budgetDataStore(t)
	_, err := s.WriteDB().ExecContext(t.Context(), "DROP TABLE wake_budget_counters")
	require.NoError(t, err)
	_, err = s.WakeBudgetCounters(t.Context())
	require.Error(t, err)
}

func modelValueName(v any) string {
	if v == nil {
		return "null"
	}
	if _, ok := v.(string); ok {
		return "text"
	}
	return "negative"
}

func TestWakeBudgetSource_UnavailableDatabase_NeverReturnsNewIdentity(t *testing.T) {
	for _, name := range []string{"cancelled", "closed", "moved"} {
		t.Run(name, func(t *testing.T) {
			s, _ := budgetDataStore(t)
			ctx := t.Context()
			switch name {
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "closed":
				require.NoError(t, s.Close())
			case "moved":
				if runtime.GOOS == "windows" {
					t.Skip("Windows refuses renaming open SQLite files")
				}
				var path string
				require.NoError(t, s.ReadDB().QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name=?", "main").Scan(&path))
				moved := filepath.Join(filepath.Dir(path), "moved.db")
				require.NoError(t, os.Rename(path, moved))
				t.Cleanup(func() { require.NoError(t, os.Rename(moved, path)) })
			}
			source, err := s.WakeBudgetSource(ctx)
			require.Error(t, err)
			require.Empty(t, source)
			_, err = s.WakeBudgetCounters(ctx)
			require.Error(t, err)
		})
	}
}
