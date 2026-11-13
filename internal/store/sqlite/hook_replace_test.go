// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests hook bookkeeping when replace frees journal sequence numbers.
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestHookReplace_TrimsOnlyStateAboveSurvivingTail(t *testing.T) {
	for _, tt := range []struct {
		name  string
		empty bool
		floor string
	}{
		{"above tail", false, "above"}, {"below tail", false, "below"},
		{"at tail", false, "tail"}, {"no cursor", false, "absent"},
		{"empty journal", true, "above"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := retentionStore(t)
			addressTo(t, s, "worker", "PROJ-1")
			kept, err := s.JournalTail(ctx)
			require.NoError(t, err)
			addressTo(t, s, "worker", "PROJ-2")
			dropped, err := s.JournalTail(ctx)
			require.NoError(t, err)
			tail := kept
			if tt.empty {
				tail = 0
			}
			floor := map[string]int64{"above": dropped, "below": kept - 1, "tail": kept}[tt.floor]
			if tt.floor != "absent" {
				_, err = raw.Exec(`INSERT INTO hook_dispatch_cursor(id,cursor) VALUES(1,?)`, floor)
				require.NoError(t, err)
			}
			for _, seq := range []int64{kept, dropped} {
				for _, outcome := range []string{sqlite.OutcomeClaimed, sqlite.OutcomeDelivered} {
					_, err = raw.Exec(`INSERT INTO hook_dispatch_ledger(hook_name,event_seq,fired_at,outcome) VALUES(?,?,?,?)`, outcome, seq, "2026-01-01T00:00:00Z", outcome)
					require.NoError(t, err)
				}
			}
			data, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			data = withoutExportNode(data, "PROJ-2")
			if tt.empty {
				data = withoutExportNode(data, "PROJ-1")
			}
			require.NoError(t, sqlite.RecomputeExportChecksum(data))
			_, err = s.Import(ctx, data, sqlite.ImportModeReplace, tt.empty)
			require.NoError(t, err)
			actualTail, err := s.JournalTail(ctx)
			require.NoError(t, err)
			require.Equal(t, tail, actualTail)
			var invalid int
			require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM hook_dispatch_ledger WHERE event_seq > ?`, tail).Scan(&invalid))
			assert.Zero(t, invalid, "no dispatch row may cover a freed sequence")
			var cursors int
			require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM hook_dispatch_cursor`).Scan(&cursors))
			if tt.floor == "absent" {
				assert.Zero(t, cursors)
			} else {
				got, err := s.HookCursor(ctx)
				require.NoError(t, err)
				assert.Equal(t, min(floor, tail), got, "lower cursors must never advance")
			}
			if !tt.empty {
				for _, outcome := range []string{sqlite.OutcomeClaimed, sqlite.OutcomeDelivered} {
					var gotOutcome, stamp string
					require.NoError(t, raw.QueryRow(`SELECT outcome,fired_at FROM hook_dispatch_ledger WHERE hook_name=? AND event_seq=?`, outcome, kept).Scan(&gotOutcome, &stamp))
					assert.Equal(t, outcome, gotOutcome)
					assert.Equal(t, "2026-01-01T00:00:00Z", stamp)
				}
			}
			addressTo(t, s, "worker", "PROJ-3")
			fresh, err := s.JournalTail(ctx)
			require.NoError(t, err)
			require.LessOrEqual(t, fresh, dropped, "premise: freed sequences reused")
			for _, hook := range []string{sqlite.OutcomeClaimed, sqlite.OutcomeDelivered} {
				won, err := s.ClaimHookDispatch(ctx, hook, fresh, time.Minute)
				require.NoError(t, err)
				assert.True(t, won)
				won, err = s.ClaimHookDispatch(ctx, hook, fresh, time.Minute)
				require.NoError(t, err)
				assert.False(t, won)
			}
		})
	}
}

func TestHookReplace_CleanupFailureRollsBackAllState(t *testing.T) {
	for _, tt := range []struct{ name, trigger string }{
		{"ledger delete", `CREATE TRIGGER refuse_hook_trim BEFORE DELETE ON hook_dispatch_ledger BEGIN SELECT RAISE(ABORT,'forced hook trim failure'); END`},
		{"inbox cursor update", `CREATE TRIGGER refuse_inbox_cursor BEFORE UPDATE ON agent_inbox_cursor BEGIN SELECT RAISE(ABORT,'forced hook trim failure'); END`},
		{"cursor update", `CREATE TRIGGER refuse_cursor_trim BEFORE UPDATE ON hook_dispatch_cursor BEGIN SELECT RAISE(ABORT,'forced hook trim failure'); END`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := retentionStore(t)
			addressTo(t, s, "worker", "PROJ-1")
			addressTo(t, s, "worker", "PROJ-2")
			tail, err := s.JournalTail(ctx)
			require.NoError(t, err)
			require.NoError(t, s.InboxAckThrough(ctx, "worker", tail))
			_, err = raw.Exec(`INSERT INTO hook_dispatch_cursor(id,cursor) VALUES(1,?)`, tail)
			require.NoError(t, err)
			_, err = raw.Exec(`INSERT INTO hook_dispatch_ledger(hook_name,event_seq,fired_at,outcome) VALUES('kept',?,'2026-01-01T00:00:00Z','delivered')`, tail)
			require.NoError(t, err)
			bookkeepingBefore := hookReplaceBookkeeping(t, raw)
			before, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			data, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			data = withoutExportNode(data, "PROJ-2")
			require.NoError(t, sqlite.RecomputeExportChecksum(data))
			_, err = raw.Exec(tt.trigger)
			require.NoError(t, err)
			_, err = s.Import(ctx, data, sqlite.ImportModeReplace, false)
			require.ErrorContains(t, err, "forced hook trim failure")
			after, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			assert.Equal(t, bookkeepingBefore, hookReplaceBookkeeping(t, raw), "cursor and ledger rows must be byte-identical after rollback")
			assert.Equal(t, before.Nodes, after.Nodes)
			assert.Equal(t, before.Dependencies, after.Dependencies)
			got, err := s.JournalTail(ctx)
			require.NoError(t, err)
			assert.Equal(t, tail, got)
			got, err = s.HookCursor(ctx)
			require.NoError(t, err)
			assert.Equal(t, tail, got)
			var stamp, outcome string
			require.NoError(t, raw.QueryRow(`SELECT fired_at,outcome FROM hook_dispatch_ledger WHERE hook_name='kept' AND event_seq=?`, tail).Scan(&stamp, &outcome))
			assert.Equal(t, "2026-01-01T00:00:00Z", stamp)
			assert.Equal(t, sqlite.OutcomeDelivered, outcome)
			var inboxCursor int64
			require.NoError(t, raw.QueryRow(`SELECT cursor FROM agent_inbox_cursor WHERE agent_id='worker'`).Scan(&inboxCursor))
			assert.Equal(t, tail, inboxCursor)
		})
	}
}

// hookReplaceBookkeeping captures all cursor and ledger values in stable order.
func hookReplaceBookkeeping(t *testing.T, raw testReadQuerier) []string {
	t.Helper()
	var out []string
	for _, query := range []string{
		`SELECT json_group_array(json_array(id,cursor)) FROM (SELECT id,cursor FROM hook_dispatch_cursor ORDER BY id)`,
		`SELECT json_group_array(json_array(hook_name,event_seq,fired_at,outcome)) FROM (SELECT hook_name,event_seq,fired_at,outcome FROM hook_dispatch_ledger ORDER BY hook_name,event_seq)`,
	} {
		var value string
		require.NoError(t, raw.QueryRowContext(context.Background(), query).Scan(&value))
		out = append(out, value)
	}
	return out
}
