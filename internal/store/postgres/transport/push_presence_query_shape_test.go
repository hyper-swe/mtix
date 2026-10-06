// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// PG-gated tests of the presence check's cost (MTIX-95.3): one query per
// batch, whatever the number of events whose INSERT inserted no row, served
// from the primary key. Skip when MTIX_PG_TEST_DSN is unset.

// countingTx counts the statements run through a transaction.
type countingTx struct {
	pgx.Tx
	statements int
}

// Query counts and runs a query.
func (c *countingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.statements++
	return c.Tx.Query(ctx, sql, args...)
}

// QueryRow counts and runs a single-row query.
func (c *countingTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.statements++
	return c.Tx.QueryRow(ctx, sql, args...)
}

// Exec counts and runs a statement.
func (c *countingTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.statements++
	return c.Tx.Exec(ctx, sql, args...)
}

// shapeEvent returns a valid update_field event number i on node MTIX-1.
func shapeEvent(i int) *model.SyncEvent {
	return &model.SyncEvent{
		EventID:       fmt.Sprintf("0193fa00-0000-7000-8000-%012d", 953000+i),
		ProjectPrefix: "MTIX", NodeID: "MTIX-1", OpType: model.OpUpdateField,
		Payload:     json.RawMessage(fmt.Sprintf(`{"field_name":"title","new_value":"v%d"}`, i)),
		WallClockTS: time.Now().UnixMilli(), LamportClock: int64(i + 1),
		VectorClock: model.VectorClock{"alice": int64(i + 1)},
		AuthorID:    "alice", AuthorMachineHash: "0123456789abcdef",
	}
}

// TestAckPresent_OneQueryPerBatch: the presence check reads every
// not-inserted event of a batch with a single query, acknowledges the ones
// the hub holds, in batch order, and leaves an id the hub does not hold
// unacknowledged; with no not-inserted event it runs no query.
func TestAckPresent_OneQueryPerBatch(t *testing.T) {
	pool := queryShapePool(t)
	ctx := context.Background()
	onHub := []*model.SyncEvent{shapeEvent(1), shapeEvent(2), shapeEvent(3), shapeEvent(4)}
	res, err := pool.PushEventsResult(ctx, onHub)
	require.NoError(t, err)
	require.Len(t, res.Inserted, len(onHub))

	tests := []struct {
		name        string
		notInserted []*model.SyncEvent
		wantQueries int
		wantPresent []string
	}{
		{"no event to check", nil, 0, nil},
		{"four events on the hub and one that is not", append(append([]*model.SyncEvent{}, onHub...), shapeEvent(9)),
			1, []string{onHub[0].EventID, onHub[1].EventID, onHub[2].EventID, onHub[3].EventID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := pool.p.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(ctx) }()
			counted := &countingTx{Tx: tx}
			acc := &pushAccum{notInserted: tt.notInserted}
			require.NoError(t, ackPresent(ctx, counted, acc))
			require.Equal(t, tt.wantQueries, counted.statements, "one query per batch")
			require.Equal(t, tt.wantPresent, acc.present)
			require.Empty(t, acc.mismatches)
		})
	}
}

// explainPresence is the EXPLAIN of the exact presence query push runs.
const explainPresence = `EXPLAIN 
SELECT p.n, h.node_id, h.op_type, h.payload = p.payload::jsonb
FROM unnest($1::text[], $2::text[]) WITH ORDINALITY AS p(event_id, payload, n)
JOIN sync_events AS h ON h.event_id = p.event_id
WHERE h.event_id = ANY($1)`

// TestPresenceQuery_PlanUsesPrimaryKey: the presence query can be served
// from sync_events' primary key, bounded by the batch's ids, so its cost
// does not grow with the hub log.
func TestPresenceQuery_PlanUsesPrimaryKey(t *testing.T) {
	pool := queryShapePool(t)
	ids := []string{"0193fa00-0000-7000-8000-000000953001", "0193fa00-0000-7000-8000-000000953002"}
	payloads := []string{`{"a":1}`, `{"b":2}`}
	plan := explainWithoutSeqScan(t, pool, explainPresence, ids, payloads)
	require.Regexp(t, regexp.MustCompile(`sync_events_pkey[^\n]*\n\s+Index Cond: \(+(h\.)?event_id = `), plan, plan)
}
