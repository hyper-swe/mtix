// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// FR-19.1 / MTIX-112.49: missing, extra, agent-key and rowid damage all refuse.
func TestCompareAckKeys_IndexDisagreesWithTable_RefusesWithConflict(t *testing.T) {
	a := ackKey{rowid: 1, agent: "alpha", seq: 10}
	b := ackKey{rowid: 2, agent: "alpha", seq: 20}
	tests := []struct {
		name       string
		index, tbl []ackKey
		wantErr    bool
	}{
		{"agree", []ackKey{a, b}, []ackKey{a, b}, false},
		{"both empty", nil, nil, false},
		{"missing index entry", []ackKey{a}, []ackKey{a, b}, true},
		{"extra index entry", []ackKey{a, b}, []ackKey{a}, true},
		{"agent key damaged", []ackKey{{1, "alphb", 10}, b}, []ackKey{a, b}, true},
		{"seq key damaged", []ackKey{{1, "alpha", 11}, b}, []ackKey{a, b}, true},
		{"rowid pointer damaged", []ackKey{{9, "alpha", 10}, b}, []ackKey{a, b}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := compareAckKeys(tt.index, tt.tbl)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, model.ErrConflict)
			require.Contains(t, err.Error(), "mtix recover")
			require.NotContains(t, err.Error(), "mtix doctor")
		})
	}
}

// FR-19.1 / MTIX-112.49: a cancelled or expired context is not index damage.
func TestCheckAckIndex_ContextEnded_ReturnsContextErrorNotConflict(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec(`CREATE TABLE agent_inbox_ack (agent_id TEXT NOT NULL, event_seq INTEGER NOT NULL, acked_at TEXT NOT NULL, PRIMARY KEY (agent_id, event_seq))`)
	require.NoError(t, err)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	for name, tc := range map[string]struct {
		ctx  context.Context
		want error
	}{"cancel": {cancelled, context.Canceled}, "deadline": {expired, context.DeadlineExceeded}} {
		t.Run(name, func(t *testing.T) {
			err := checkAckIndexAgreesWithTable(tc.ctx, db)
			require.Error(t, err)
			require.True(t, errors.Is(err, tc.want), err.Error())
			require.False(t, errors.Is(err, model.ErrConflict), err.Error())
			require.NotContains(t, err.Error(), "mtix recover")
		})
	}
}
