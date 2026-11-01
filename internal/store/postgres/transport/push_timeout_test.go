// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// These tests stall the real push after its INSERT, leaving the backend idle.
// They require a dedicated throwaway MTIX_PG_TEST_DSN and take about a minute.
type stalledPushTracer struct {
	t       *testing.T
	stalled *atomic.Bool
}
type pushInsertKey struct{}

// 6 s past the 60 s idle bound, so a slow backend timer cannot lose the race.
const pushStallDuration = 66 * time.Second

func (s stalledPushTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, pushInsertKey{}, strings.Contains(data.SQL, "INSERT INTO sync_events"))
}

func (s stalledPushTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	if ctx.Value(pushInsertKey{}) != true || data.Err != nil {
		return
	}
	var idle int64
	require.NoError(s.t, conn.QueryRow(ctx, `SELECT setting::bigint FROM pg_settings WHERE name = 'idle_in_transaction_session_timeout'`).Scan(&idle))
	require.Equal(s.t, int64(60000), idle)
	var transaction *string
	require.NoError(s.t, conn.QueryRow(ctx, `SELECT current_setting($1, true)`, "transaction_timeout").Scan(&transaction))
	if transaction != nil {
		require.Equal(s.t, "10min", *transaction)
	}
	time.Sleep(pushStallDuration)
	s.stalled.Store(true)
}

// isHubTimeoutAbort reports whether err is the hub ending the stalled push: the
// timeout SQLSTATE, or the connection the server closed after it.
func isHubTimeoutAbort(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "25P03" || pgErr.Code == "25P04"
	}
	return pgconn.Timeout(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(err.Error(), "conn closed")
}

func TestPushTransaction_StalledInsertAborted(t *testing.T) {
	p := settingTestPool(t, 10*time.Second)
	ctx := context.Background()
	require.NoError(t, p.Migrate(ctx))
	cfg := p.p.Config()
	var stallRan atomic.Bool
	cfg.ConnConfig.Tracer = stalledPushTracer{t: t, stalled: &stallRan}
	inner, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer inner.Close()
	stalled := &Pool{p: inner}
	e := &model.SyncEvent{EventID: "0193fa00-0000-7000-8000-000000107029", ProjectPrefix: "TEST",
		NodeID: "TEST-10729", OpType: model.OpTransitionStatus, Payload: []byte(`{}`), WallClockTS: 1,
		LamportClock: 1, VectorClock: model.VectorClock{"test": 1}, AuthorID: "test", AuthorMachineHash: "0123456789abcdef"}
	_, err = stalled.pushEventsOnce(ctx, []*model.SyncEvent{e})
	require.True(t, stallRan.Load(), "the stall after the INSERT must have run, or the test proves nothing")
	require.Error(t, err, "hub must abort the idle push before COMMIT")
	require.True(t, isHubTimeoutAbort(err), "error must be the hub timeout, not another failure: %v", err)
	var count int
	require.NoError(t, p.p.QueryRow(ctx, `SELECT count(*) FROM sync_events WHERE event_id = $1`, e.EventID).Scan(&count))
	require.Zero(t, count, "aborted push must leave no committed event")
}
