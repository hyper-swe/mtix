// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that storage failures cannot partially finalize hook bookkeeping.
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func hookFenceOperation(ctx context.Context, s *sqlite.Store, events []sqlite.JournalEvent, operation string) error {
	event := events[len(events)-1]
	switch operation {
	case "claim", "reclaim":
		_, _, err := s.ClaimHookDispatchEvent(ctx, "fenced", event, time.Minute)
		return err
	case "outcome":
		_, err := s.RecordHookDispatchEventOutcome(ctx, "fenced", event, sqlite.OutcomeDelivered)
		return err
	case "floor":
		_, err := s.AdvanceHookScanFloorForEvents(ctx, 0, events)
		return err
	case "inbox":
		_, err := s.RecordInboxDeliveryEvent(ctx, "recipient", event, "fenced")
		return err
	case "empty agent":
		_, err := s.RecordInboxDeliveryEvent(ctx, "", event, "fenced")
		return err
	default:
		panic("unknown test operation")
	}
}

func TestHookFence_WriteFailureRollsBack(t *testing.T) {
	for _, tt := range []struct{ operation, trigger string }{
		{"claim", `CREATE TRIGGER fail_claim BEFORE INSERT ON hook_dispatch_ledger BEGIN SELECT RAISE(ABORT,'forced fence write failure'); END`},
		{"reclaim", `CREATE TRIGGER fail_reclaim BEFORE UPDATE ON hook_dispatch_ledger BEGIN SELECT RAISE(ABORT,'forced fence write failure'); END`},
		{"outcome", `CREATE TRIGGER fail_outcome BEFORE UPDATE ON hook_dispatch_ledger BEGIN SELECT RAISE(ABORT,'forced fence write failure'); END`},
		{"floor", `CREATE TRIGGER fail_floor BEFORE UPDATE ON hook_dispatch_cursor BEGIN SELECT RAISE(ABORT,'forced fence write failure'); END`},
		{"inbox", `CREATE TRIGGER fail_inbox BEFORE INSERT ON inbox_deliveries BEGIN SELECT RAISE(ABORT,'forced fence write failure'); END`},
	} {
		t.Run(tt.operation, func(t *testing.T) {
			ctx := context.Background()
			s, raw := retentionStore(t)
			addressTo(t, s, "worker", "PROJ-1")
			events, err := s.ReadJournalSince(ctx, 0, 500)
			require.NoError(t, err)
			if tt.operation == "reclaim" || tt.operation == "outcome" {
				won, err := s.ClaimHookDispatch(ctx, "fenced", events[len(events)-1].Seq, time.Minute)
				require.NoError(t, err)
				require.True(t, won)
				backdateClaim(t, s, "fenced", events[len(events)-1].Seq, time.Hour)
			}
			require.NoError(t, s.AdvanceHookScanFloorClamped(ctx, 0))
			before := hookReplaceBookkeeping(t, raw)
			_, err = raw.Exec(tt.trigger)
			require.NoError(t, err)
			err = hookFenceOperation(ctx, s, events, tt.operation)
			require.ErrorContains(t, err, "forced fence write failure")
			assert.Equal(t, before, hookReplaceBookkeeping(t, raw))
			var delivered int
			require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM inbox_deliveries`).Scan(&delivered))
			assert.Zero(t, delivered)
		})
	}
}

func TestHookFence_IdentityReadFailureIsReported(t *testing.T) {
	for _, operation := range []string{"claim", "outcome", "floor", "inbox"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			s, raw := retentionStore(t)
			addressTo(t, s, "worker", "PROJ-1")
			events, err := s.ReadJournalSince(ctx, 0, 500)
			require.NoError(t, err)
			// A damaged journal schema must be reported, never interpreted as a miss.
			_, err = raw.Exec(`ALTER TABLE sync_events RENAME COLUMN event_id TO hidden_event_id`)
			require.NoError(t, err)
			require.ErrorContains(t, hookFenceOperation(ctx, s, events, operation), "event_id")
		})
	}
}

func TestHookFence_EmptyInboxRecipientIsInvalid(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	addressTo(t, s, "worker", "PROJ-1")
	events, err := s.ReadJournalSince(ctx, 0, 500)
	require.NoError(t, err)
	require.ErrorIs(t, hookFenceOperation(ctx, s, events, "empty agent"), model.ErrInvalidInput)
}
