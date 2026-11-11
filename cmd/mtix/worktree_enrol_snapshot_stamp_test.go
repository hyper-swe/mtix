// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestEnrolWorktree_RealPath_SnapshotStampIsNeverExcused (MTIX-112.17.16.32):
// the one-second rule excuses only a stamp that a legacy 0.5.4 terminal event
// produced, an event with no closed_at. An event from 0.6.0 or later carries a
// closed_at snapshot, so its stamp compares exactly: a gap of one second in
// either direction, the very size the rule tolerates for a legacy event, is a
// real difference and refuses, naming closed_at. A snapshot equal to the store's
// stamp is the control and enrols.
func TestEnrolWorktree_RealPath_SnapshotStampIsNeverExcused(t *testing.T) {
	tests := []struct {
		name      string
		gap       time.Duration // snapshot closed_at relative to the store's stamp
		wantEnrol bool
	}{
		{"snapshot equal to the store stamp", 0, true},
		{"snapshot one second after", time.Second, false},
		{"snapshot one second before", -time.Second, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
			var stored string
			require.NoError(t, writer.WriteDB().QueryRowContext(ctx, `SELECT closed_at FROM nodes WHERE id = 'TEST-4'`).Scan(&stored))
			require.NoError(t, writer.Close())
			storeAt, err := time.Parse(time.RFC3339, stored)
			require.NoError(t, err)
			snapshotAt := storeAt.Add(tt.gap)
			done := doneEventOf(t, hubEvents, "TEST-4")
			done.WallClockTS = snapshotAt.UnixMilli()
			var p map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(done.Payload, &p))
			p["closed_at"], err = json.Marshal(snapshotAt.UTC().Format(time.RFC3339))
			require.NoError(t, err)
			done.Payload, err = json.Marshal(p)
			require.NoError(t, err)
			svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))

			_, err = svc.EnrolWorktree(ctx, enrolRequest(root))

			if tt.wantEnrol {
				require.NoError(t, err)
				assert.True(t, *recorded)
				return
			}
			var refusal *service.WorktreeBoardError
			require.ErrorAs(t, err, &refusal)
			assert.Contains(t, refusal.Diff["TEST-4"], "closed_at")
			assert.False(t, *recorded)
		})
	}
}
