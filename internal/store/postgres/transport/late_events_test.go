// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Tests for the hub reads behind the late-event sweep of `mtix sync pull`
// (MTIX-95.5; ADR-006 D5): listing hub event ids by created_at window or
// over the full history, with the hub's clock, and fetching events by id.
// PG-bound tests skip when MTIX_PG_TEST_DSN is unset.

// lateEventID returns a deterministic event id for index i.
func lateEventID(i int) string {
	return fmt.Sprintf("0193fb00-0000-7000-8000-%012d", i)
}

// pushLateEvents pushes n create events (lamport = i) and returns their ids.
func pushLateEvents(t *testing.T, pool *transport.Pool, n int) []string {
	t.Helper()
	events := make([]*model.SyncEvent, n)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = lateEventID(i + 1)
		events[i] = makeEvent(ids[i], fmt.Sprintf("MTIX-%d", i+1), "alice", int64(i+1))
	}
	accepted, _, err := pool.PushEvents(context.Background(), events)
	require.NoError(t, err)
	require.Len(t, accepted, n)
	return ids
}

// setCreatedAt pins one hub event's created_at so tests control the order.
func setCreatedAt(t *testing.T, pool *transport.Pool, id string, at time.Time) {
	t.Helper()
	tag, err := pool.Inner().Exec(context.Background(),
		`UPDATE sync_events SET created_at = $1 WHERE event_id = $2`, at, id)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected())
}

// hubClock reads the hub's now().
func hubClock(t *testing.T, pool *transport.Pool) time.Time {
	t.Helper()
	var now time.Time
	require.NoError(t, pool.Inner().QueryRow(context.Background(), `SELECT now()`).Scan(&now))
	return now
}

// collectSince pages ListEventIDsSince from start to the end and returns
// every id in order plus the number of pages read.
func collectSince(t *testing.T, pool *transport.Pool, start time.Time, limit int) ([]string, int) {
	t.Helper()
	var ids []string
	cursor := transport.EventIDCursor{CreatedAt: start}
	for pages := 1; ; pages++ {
		page, err := pool.ListEventIDsSince(context.Background(), cursor, limit)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.IDs), limit)
		ids = append(ids, page.IDs...)
		if !page.More {
			return ids, pages
		}
		cursor = page.Next
	}
}

// TestListEventIDsSince_EmptyHub_ReturnsHubClock: an empty window still
// returns the hub's clock, so a sweep can record its hub time without a
// separate query.
func TestListEventIDsSince_EmptyHub_ReturnsHubClock(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	before := hubClock(t, pool)
	page, err := pool.ListEventIDsSince(context.Background(),
		transport.EventIDCursor{CreatedAt: before.Add(-time.Hour)}, 10)
	after := hubClock(t, pool)

	require.NoError(t, err)
	require.Empty(t, page.IDs)
	require.False(t, page.More)
	require.False(t, page.HubNow.Before(before), "HubNow %s before %s", page.HubNow, before)
	require.False(t, page.HubNow.After(after), "HubNow %s after %s", page.HubNow, after)
}

// TestListEventIDsSince_PagesInCreatedAtOrder: the window listing returns
// the ids created at or after the start, in (created_at, event_id) order,
// across pages, with no id repeated or skipped, including ids that share a
// created_at across a page boundary.
func TestListEventIDsSince_PagesInCreatedAtOrder(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))
	ids := pushLateEvents(t, pool, 6)
	base := hubClock(t, pool).Add(-time.Hour).Truncate(time.Second)
	// ids[0] is before the window; ids[1] is exactly at its start; ids[2..4]
	// share one created_at; ids[5] was created first of the in-window ids
	// by the clock, so it sorts before them despite its larger event id.
	setCreatedAt(t, pool, ids[0], base.Add(-time.Second))
	setCreatedAt(t, pool, ids[1], base)
	setCreatedAt(t, pool, ids[5], base.Add(time.Second))
	for _, id := range ids[2:5] {
		setCreatedAt(t, pool, id, base.Add(2*time.Second))
	}

	tests := []struct {
		name      string
		limit     int
		wantPages int
	}{
		{"one page", 10, 1},
		{"page boundary inside a shared created_at", 2, 3},
		{"page of one", 1, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, pages := collectSince(t, pool, base, tt.limit)
			require.Equal(t, []string{ids[1], ids[5], ids[2], ids[3], ids[4]}, got)
			require.Equal(t, tt.wantPages, pages)
		})
	}
}

// TestListAllEventIDs_PagesInEventIDOrder: the full-history listing returns
// every hub id in event_id order across pages, whatever their created_at.
func TestListAllEventIDs_PagesInEventIDOrder(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))
	ids := pushLateEvents(t, pool, 5)
	setCreatedAt(t, pool, ids[3], hubClock(t, pool).Add(-400*24*time.Hour))

	before := hubClock(t, pool)
	var got []string
	after := ""
	pages := 0
	for {
		page, err := pool.ListAllEventIDs(context.Background(), after, 2)
		require.NoError(t, err)
		pages++
		if pages == 1 {
			require.False(t, page.HubNow.Before(before), "first page carries the hub clock")
		}
		got = append(got, page.IDs...)
		if !page.More {
			break
		}
		after = page.Next.EventID
	}
	require.Equal(t, ids, got)
	require.Equal(t, 3, pages)
}

// TestFetchEventsByID_ReturnsFullEvents: fetching by id returns the full
// hub rows for the ids the hub holds, in Lamport order, and ignores ids it
// does not hold.
func TestFetchEventsByID_ReturnsFullEvents(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))
	ids := pushLateEvents(t, pool, 4)

	got, err := pool.FetchEventsByID(context.Background(),
		[]string{ids[3], "0193fb00-0000-7000-8000-999999999999", ids[1]})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, ids[1], got[0].EventID)
	require.Equal(t, ids[3], got[1].EventID)
	require.Equal(t, int64(2), got[0].LamportClock)
	require.Equal(t, "MTIX-2", got[0].NodeID)
	require.Equal(t, model.OpCreateNode, got[0].OpType)
	require.Equal(t, "alice", got[0].AuthorID)
	require.Equal(t, model.VectorClock{"alice": 2}, got[0].VectorClock)
	require.JSONEq(t, `{"title":"x"}`, string(got[0].Payload))
	require.False(t, got[0].CreatedAt.IsZero(), "created_at is carried")

	none, err := pool.FetchEventsByID(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, none)
}

// TestMigrate_CreatesSyncEventsCreatedAtIndex: migration 014 creates the
// created_at index, and re-running the migration set keeps exactly one.
func TestMigrate_CreatesSyncEventsCreatedAtIndex(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))
	require.NoError(t, pool.Migrate(context.Background()), "re-run is a no-op")

	var n int
	var def string
	require.NoError(t, pool.Inner().QueryRow(context.Background(), `
		SELECT count(*) OVER (), indexdef FROM pg_indexes
		WHERE tablename = 'sync_events' AND indexname = 'idx_sync_events_created_at'`,
	).Scan(&n, &def))
	require.Equal(t, 1, n)
	require.Contains(t, def, "(created_at)")
}

// TestLateEventReads_NilPoolAndBadLimit: the sweep reads refuse a closed
// pool and a non-positive limit before touching Postgres.
func TestLateEventReads_NilPoolAndBadLimit(t *testing.T) {
	var nilPool *transport.Pool
	open := &transport.Pool{}
	ctx := context.Background()
	tests := []struct {
		name    string
		call    func() error
		wantErr string
	}{
		{"since on nil pool", func() error {
			_, err := nilPool.ListEventIDsSince(ctx, transport.EventIDCursor{}, 10)
			return err
		}, "pool not open"},
		{"all on nil pool", func() error {
			_, err := nilPool.ListAllEventIDs(ctx, "", 10)
			return err
		}, "pool not open"},
		{"fetch on nil pool", func() error {
			_, err := nilPool.FetchEventsByID(ctx, []string{"x"})
			return err
		}, "pool not open"},
		{"since with zero limit", func() error {
			_, err := open.ListEventIDsSince(ctx, transport.EventIDCursor{}, 0)
			return err
		}, "limit must be > 0"},
		{"all with negative limit", func() error {
			_, err := open.ListAllEventIDs(ctx, "", -1)
			return err
		}, "limit must be > 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
