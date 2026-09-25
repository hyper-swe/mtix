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
// in Lamport order, which is causal, unlike the listing order, one bounded
// chunk at a time.

// applyStagedLateEvents is the apply phase. It reads the staged ids in
// (lamport_clock, event_id) order, limit at a time, and for each chunk
// fetches the events the store still lacks, applies them in pull order
// (Lamport clock, then event id), quarantining any that fails (MTIX-95.11),
// and removes each id from sync_sweep_pending in its apply's transaction. Every staged event has a Lamport clock at or
// above the previous chunk's, so the order is global although memory and
// each transaction are bounded by limit, and a pull stopped between chunks
// resumes at the next one. Staged ids the store now holds (a concurrent pull
// applied them) or the hub no longer has are removed without an apply.
// Returns how many events it applied.
func applyStagedLateEvents(ctx context.Context, in pullIngest, hub lateEventHub, st *sqlite.Store, limit int) (int, error) {
	applied := 0
	for {
		chunk, err := readStagedChunk(ctx, st, limit)
		if err != nil || len(chunk) == 0 {
			return applied, err
		}
		n, err := applyStagedChunk(ctx, in, hub, st, chunk, limit)
		applied += n
		if err != nil {
			return applied, err
		}
	}
}

// applyStagedChunk fetches and applies one chunk of staged ids and returns
// how many events it applied. An event that fails its checks or its apply
// is quarantined (MTIX-95.11) and unstaged in the same transaction; it is
// not counted.
func applyStagedChunk(ctx context.Context, in pullIngest, hub lateEventHub, st *sqlite.Store,
	chunk []string, limit int,
) (int, error) {
	missing, err := missingLocalEventIDs(ctx, st, chunk)
	if err != nil {
		return 0, err
	}
	events, err := fetchLateEvents(ctx, hub, missing, limit)
	if err != nil {
		return 0, err
	}
	if err := unstageLateEvents(ctx, st, idsNotApplied(chunk, events)); err != nil {
		return 0, err
	}
	unstage := func(tx *sql.Tx, e *model.SyncEvent) error {
		// Remove the event's id, applied or quarantined, in the same
		// transaction.
		_, execErr := tx.ExecContext(ctx,
			`DELETE FROM sync_sweep_pending WHERE event_id = ?`, e.EventID)
		return execErr
	}
	held, applyErr := applyPullBatch(ctx, in, st, quarantineSourceSweep, events, unstage)
	if applyErr != nil {
		return 0, fmt.Errorf("apply late events: %w", applyErr)
	}
	return len(events) - len(held), nil
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

// readStagedChunk returns up to limit staged ids, lowest Lamport clock
// first (then event id), served by idx_sync_sweep_pending_lamport.
func readStagedChunk(ctx context.Context, st *sqlite.Store, limit int) ([]string, error) {
	// The next chunk of the apply phase: the lowest staged Lamport clocks.
	rows, err := st.Query(ctx, `
		SELECT event_id FROM sync_sweep_pending
		ORDER BY lamport_clock, event_id
		LIMIT ?`, limit)
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
