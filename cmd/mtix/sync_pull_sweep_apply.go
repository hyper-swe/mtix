// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Apply phase of the late-event sweep of `mtix sync pull` (MTIX-95.5; see
// sync_pull_sweep.go). It runs once the listing phase has staged every
// missing hub event id in sync_sweep_pending, and applies the staged events
// in Lamport order, which is causal, unlike the listing order.

// applyStagedLateEvents is the apply phase: it fetches every staged event
// the store still lacks, sorts them all into pull order (Lamport clock, then
// event id) and applies them in batches of limit, removing each id from
// sync_sweep_pending in its apply's transaction. Staged ids the store now
// holds (a concurrent pull applied them) or the hub no longer has are
// removed without an apply. Returns how many events it applied.
func applyStagedLateEvents(ctx context.Context, hub lateEventHub, st *sqlite.Store, limit int) (int, error) {
	staged, err := readStagedEventIDs(ctx, st)
	if err != nil || len(staged) == 0 {
		return 0, err
	}
	missing, err := missingLocalEventIDs(ctx, st, staged)
	if err != nil {
		return 0, err
	}
	events, err := fetchLateEvents(ctx, hub, missing, limit)
	if err != nil {
		return 0, err
	}
	if err := unstageLateEvents(ctx, st, idsNotApplied(staged, events)); err != nil {
		return 0, err
	}
	unstage := func(tx *sql.Tx, e *model.SyncEvent) error {
		// Remove the applied event's id, in its apply's transaction.
		_, execErr := tx.ExecContext(ctx,
			`DELETE FROM sync_sweep_pending WHERE event_id = ?`, e.EventID)
		return execErr
	}
	applied := 0
	for start := 0; start < len(events); start += limit {
		batch := events[start:min(start+limit, len(events))]
		if err := applyPullBatch(ctx, st, batch, unstage); err != nil {
			return applied, fmt.Errorf("apply late events: %w", err)
		}
		applied += len(batch)
	}
	return applied, nil
}

// idsNotApplied returns the staged ids that have no fetched event to apply.
func idsNotApplied(staged []string, events []*model.SyncEvent) []string {
	fetched := make(map[string]bool, len(events))
	for _, e := range events {
		fetched[e.EventID] = true
	}
	var out []string
	for _, id := range staged {
		if !fetched[id] {
			out = append(out, id)
		}
	}
	return out
}

// readStagedEventIDs returns every id in sync_sweep_pending, in event id
// order.
func readStagedEventIDs(ctx context.Context, st *sqlite.Store) ([]string, error) {
	rows, err := st.Query(ctx, `SELECT event_id FROM sync_sweep_pending ORDER BY event_id`)
	if err != nil {
		return nil, fmt.Errorf("read staged late events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read staged late events: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read staged late events: %w", err)
	}
	return ids, nil
}

// unstageLateEvents removes ids from sync_sweep_pending in one transaction.
func unstageLateEvents(ctx context.Context, st *sqlite.Store, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return st.WithTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			// Drop one staged id that needs no apply.
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM sync_sweep_pending WHERE event_id = ?`, id); err != nil {
				return fmt.Errorf("unstage %s: %w", id, err)
			}
		}
		return nil
	})
}

// fetchLateEvents fetches ids from the hub in chunks of limit and returns
// the events sorted into pull order (Lamport clock, then event id) across
// all chunks, so a recovered create applies before any recovered edit of
// the same node: an edit is always stamped above the create it follows.
func fetchLateEvents(ctx context.Context, hub lateEventHub, ids []string, limit int) ([]*model.SyncEvent, error) {
	var events []*model.SyncEvent
	for start := 0; start < len(ids); start += limit {
		chunk, err := hub.FetchEventsByID(ctx, ids[start:min(start+limit, len(ids))])
		if err != nil {
			return nil, fmt.Errorf("fetch late events: %w", err)
		}
		events = append(events, chunk...)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].LamportClock != events[j].LamportClock {
			return events[i].LamportClock < events[j].LamportClock
		}
		return events[i].EventID < events[j].EventID
	})
	return events, nil
}
