// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// The hub stores payloads as JSONB, which keeps a number by value: 1e0 comes
// back as 1. A matching hub copy acknowledges the relay forward whatever the
// number's spelling; adjacent large integers are different values and stay
// pending for push's integrity check.
func TestAcknowledgeRelayForward_NumbersCompareByValue(t *testing.T) {
	cases := []struct {
		name, local, hub string
		acked            bool
	}{
		{"exponent form", `{"title":"t","n":1e0}`, `{"title":"t","n":1}`, true},
		{"fraction and exponent", `{"title":"t","n":[0.50,1E2]}`, `{"title":"t","n":[0.5,100]}`, true},
		{"adjacent large integers", `{"title":"t","n":12345678901234567890}`, `{"title":"t","n":12345678901234567891}`, false},
		{"large integer exponent", `{"title":"t","n":12345678901234567890}`, `{"title":"t","n":1.234567890123456789e19}`, true},
		{"huge exponent", `{"title":"t","n":1e999999999}`, `{"title":"t","n":1}`, false},
		{"different text", `{"title":"t","n":1}`, `{"title":"u","n":1}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := t.Context()
			s := newTestStore(t)
			e := &model.SyncEvent{
				EventID: clock.MustNewEventID(), ProjectPrefix: "PROJ", NodeID: "PROJ-1", OpType: model.OpCreateNode,
				Payload: []byte(c.local), WallClockTS: 1, LamportClock: 1,
				VectorClock: model.VectorClock{"p": 1}, AuthorID: "p", AuthorMachineHash: "aaaaaaaaaaaaaaaa",
			}
			e.UID = e.EventID
			require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error { return s.ApplyRelayEvent(ctx, tx, e) }))
			hub := *e
			hub.Payload = []byte(c.hub)
			require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error { return s.IdempotentApply(ctx, tx, &hub) }))
			pending, err := s.ReadPendingEvents(ctx, 10)
			require.NoError(t, err)
			if c.acked {
				require.Empty(t, pending)
			} else {
				require.Len(t, pending, 1)
			}
		})
	}
}
