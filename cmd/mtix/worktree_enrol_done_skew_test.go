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

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// doneEventOf returns the hub's done transition of the node.
func doneEventOf(t *testing.T, events []*model.SyncEvent, node string) *model.SyncEvent {
	t.Helper()
	for _, e := range events {
		if e.OpType != model.OpTransitionStatus || e.NodeID != node {
			continue
		}
		var p model.TransitionStatusPayload
		require.NoError(t, json.Unmarshal(e.Payload, &p))
		if p.To == model.StatusDone {
			return e
		}
	}
	require.FailNow(t, "no done event for "+node)
	return nil
}

// TestEnrolWorktree_RealPath_DoneStampAcrossASecond (MTIX-112.17.16.19.1): a
// plain 0.5.4 done event carries no closed_at; the store stamped closed_at from
// its own clock read and the event carries a second wall-clock read, so the two
// can fall either side of a second. A clone stamps closed_at from the event.
// Only a legacy event within one second of the store's stamp is skew; a gap of
// two seconds, or an event that carries its own closed_at, is a real difference.
func TestEnrolWorktree_RealPath_DoneStampAcrossASecond(t *testing.T) {
	tests := []struct {
		name       string
		legacy     bool
		wallOffset time.Duration // event wall clock relative to the store's stamp
		wantEnrol  bool
	}{
		{"legacy event one second after", true, time.Second + 5*time.Millisecond, true},
		{"legacy event one second before", true, -time.Second + 500*time.Millisecond, true},
		{"legacy event two seconds after", true, 2*time.Second + 5*time.Millisecond, false},
		{"legacy event two seconds before", true, -2 * time.Second, false},
		{"event carrying closed_at one second off", false, time.Second + 5*time.Millisecond, false},
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
			if tt.legacy {
				hubEvents = preReplicatedFieldsEvents(t, hubEvents)
			}
			done := doneEventOf(t, hubEvents, "TEST-4")
			done.WallClockTS = storeAt.Add(tt.wallOffset).UnixMilli()
			if !tt.legacy {
				var p map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(done.Payload, &p))
				p["closed_at"], err = json.Marshal(storeAt.Add(tt.wallOffset).UTC().Format(time.RFC3339))
				require.NoError(t, err)
				done.Payload, err = json.Marshal(p)
				require.NoError(t, err)
			}
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
