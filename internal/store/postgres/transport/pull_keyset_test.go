// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Keyset pagination of the pull cursor pass (MTIX-95.4; ADR-006 D6, D39):
// PullEvents pages by (lamport_clock, event_id), so events that share a
// Lamport clock across a page boundary are all returned, a Lamport-only
// cursor (an upgraded client) re-reads the events at exactly its clock, and
// hub migration 015 indexes that order. PG-bound tests skip when
// MTIX_PG_TEST_DSN is unset.

// keysetIndexName is the index hub migration 015 adds for the pull keyset.
const keysetIndexName = "idx_sync_events_lamport_event_id"

// keysetEventID returns a deterministic event id; ids sort in i order.
func keysetEventID(i int) string {
	return fmt.Sprintf("0193fc00-0000-7000-8000-%012d", i)
}

// pushKeysetEvents pushes one create per Lamport clock in lamports, the
// i-th with id keysetEventID(i+1), so ids ascend in the order given whatever
// the clocks, and returns the ids in keyset order (clock, then id). The
// events are pushed in reverse, so the hub's insertion order is not the
// keyset order.
func pushKeysetEvents(t *testing.T, pool *transport.Pool, lamports ...int64) []string {
	t.Helper()
	events := make([]*model.SyncEvent, len(lamports))
	for i, l := range lamports {
		events[len(lamports)-1-i] = makeEvent(keysetEventID(i+1), fmt.Sprintf("MTIX-%d", i+1), "alice", l)
	}
	accepted, _, err := pool.PushEvents(context.Background(), events)
	require.NoError(t, err)
	require.Len(t, accepted, len(lamports))
	sort.Slice(events, func(i, j int) bool {
		if events[i].LamportClock != events[j].LamportClock {
			return events[i].LamportClock < events[j].LamportClock
		}
		return events[i].EventID < events[j].EventID
	})
	ids := make([]string, len(events))
	for i, e := range events {
		ids[i] = e.EventID
	}
	return ids
}

// pullAllPages pages PullEvents from after, limit at a time, each page
// starting after the previous page's last event, until hasMore is false. It
// returns every event id in the order received and the number of pages.
func pullAllPages(t *testing.T, pool *transport.Pool, after transport.PullCursor, limit int) ([]string, int) {
	t.Helper()
	var ids []string
	for pages := 1; pages <= 100; pages++ {
		events, hasMore, err := pool.PullEvents(context.Background(), after, limit)
		require.NoError(t, err)
		require.LessOrEqual(t, len(events), limit)
		for _, e := range events {
			ids = append(ids, e.EventID)
		}
		if !hasMore {
			return ids, pages
		}
		require.NotEmpty(t, events, "a page with more to come is not empty")
		after = transport.CursorAt(events[len(events)-1])
	}
	require.FailNow(t, "the pull cursor did not reach the end of the log in 100 pages")
	return nil, 0
}

// TestPullEvents_EqualLamportAcrossPageBoundary_NoneSkipped: events that
// share a Lamport clock are all returned when a page boundary falls among
// them, each exactly once, in (lamport_clock, event_id) order. Before
// MTIX-95.4 the next page asked for lamport_clock > the page's highest
// clock, so the events at that clock that did not fit were never pulled.
func TestPullEvents_EqualLamportAcrossPageBoundary_NoneSkipped(t *testing.T) {
	tests := []struct {
		name      string
		lamports  []int64
		limit     int
		wantPages int
	}{
		{"three events share one Lamport, limit 2", []int64{7, 7, 7}, 2, 2},
		{"three events share one Lamport, limit 1", []int64{7, 7, 7}, 1, 3},
		{"a tie between other clocks, limit 1", []int64{5, 7, 7, 7, 9}, 1, 5},
		{"a tie between other clocks, limit 2", []int64{5, 7, 7, 7, 9}, 2, 3},
		{"a tie between other clocks, limit 3", []int64{5, 7, 7, 7, 9}, 3, 2},
		{"a tie between other clocks, limit 4", []int64{5, 7, 7, 7, 9}, 4, 2},
		{"one page holds everything", []int64{5, 7, 7, 7, 9}, 100, 1},
		// Clocks 9, 5, 7, 5, 7 with ids ascending in that order: neither the
		// clock nor the id alone gives the order, and the last event (clock
		// 9) has the lowest id.
		{"interleaved clocks and ids, limit 1", []int64{9, 5, 7, 5, 7}, 1, 5},
		{"interleaved clocks and ids, limit 2", []int64{9, 5, 7, 5, 7}, 2, 3},
		{"interleaved clocks and ids, limit 3", []int64{9, 5, 7, 5, 7}, 3, 2},
		{"interleaved clocks and ids, one page", []int64{9, 5, 7, 5, 7}, 100, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := openTestPool(t)
			require.NoError(t, pool.Migrate(context.Background()))
			want := pushKeysetEvents(t, pool, tt.lamports...)

			got, pages := pullAllPages(t, pool, transport.PullCursor{}, tt.limit)

			require.Equal(t, want, got, "every event, once, in (lamport_clock, event_id) order")
			require.Equal(t, tt.wantPages, pages)
			if tt.lamports[0] == 9 {
				require.Equal(t, []string{keysetEventID(2), keysetEventID(4), keysetEventID(3),
					keysetEventID(5), keysetEventID(1)}, got, "clocks 5, 5, 7, 7, 9")
			}
		})
	}
}

// TestPullEvents_LamportOnlyCursor_ReturnsEveryEventAtThatClock: a cursor
// with an empty event id (a client upgraded from a Lamport-only cursor)
// returns every event at exactly its Lamport clock and above, and a full
// cursor returns only the events after its position.
func TestPullEvents_LamportOnlyCursor_ReturnsEveryEventAtThatClock(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))
	ids := pushKeysetEvents(t, pool, 5, 7, 7, 7, 9)

	tests := []struct {
		name  string
		after transport.PullCursor
		want  []string
	}{
		{"zero cursor: the whole log", transport.PullCursor{}, ids},
		{"Lamport-only cursor: every event at 7 again", transport.PullCursor{Lamport: 7}, ids[1:]},
		{"after the second event at 7", transport.PullCursor{Lamport: 7, EventID: ids[2]}, ids[3:]},
		{"after the last event at 7", transport.PullCursor{Lamport: 7, EventID: ids[3]}, ids[4:]},
		{"after the last event", transport.PullCursor{Lamport: 9, EventID: ids[4]}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := pullAllPages(t, pool, tt.after, 100)
			require.Equal(t, tt.want, got)
		})
	}
}

// syncEventsIndexes returns the definition of every index on sync_events,
// by name.
func syncEventsIndexes(t *testing.T, pool *transport.Pool) map[string]string {
	t.Helper()
	rows, err := pool.Inner().Query(context.Background(), `
		SELECT indexname, indexdef FROM pg_indexes WHERE tablename = 'sync_events'`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, def string
		require.NoError(t, rows.Scan(&name, &def))
		out[name] = def
	}
	require.NoError(t, rows.Err())
	return out
}

// TestMigrate_CreatesPullKeysetIndex: migration 015 creates the index on
// sync_events (lamport_clock, event_id) that serves the pull keyset, and
// re-running the migration set changes no index on sync_events.
func TestMigrate_CreatesPullKeysetIndex(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))
	first := syncEventsIndexes(t, pool)

	require.NoError(t, pool.Migrate(context.Background()), "re-run is a no-op")

	require.Contains(t, first, keysetIndexName)
	require.Contains(t, first[keysetIndexName], "(lamport_clock, event_id)")
	require.Equal(t, first, syncEventsIndexes(t, pool), "a second migrate changes no index")
}

// TestPullEvents_HubWithoutKeysetIndex_StillServesPulls: a hub whose owner
// has not re-run `mtix sync init` since upgrading lacks the index; the
// keyset pull is still correct there, only slower, and the next migrate
// creates the index.
func TestPullEvents_HubWithoutKeysetIndex_StillServesPulls(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))
	_, err := pool.Inner().Exec(context.Background(), `DROP INDEX IF EXISTS `+keysetIndexName)
	require.NoError(t, err)
	require.NotContains(t, syncEventsIndexes(t, pool), keysetIndexName, "precondition: no keyset index")
	want := pushKeysetEvents(t, pool, 5, 7, 7, 7, 9)

	got, pages := pullAllPages(t, pool, transport.PullCursor{}, 2)

	require.Equal(t, want, got)
	require.Equal(t, 3, pages)
	require.NoError(t, pool.Migrate(context.Background()))
	require.Contains(t, syncEventsIndexes(t, pool), keysetIndexName, "the next migrate creates the index")
}
