// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// releasePushHolds runs at the start of every push (MTIX-95.12) and settles
// the push holds before any event is read. A temporary clock hold is
// validated again at the store's clock, and one that passes is released.
// Then every dependent hold is checked against the subtree rule
// (sync_push_hold_rule.go), in queue order: with no held creation left
// above it (for a link or unlink, none left before it), it is validated
// with the hub's rules and released if it passes, so a creation whose
// clock hold cleared goes out in this push
// together with its whole subtree, as ordinary pending events. A dependent
// that fails stays held under its own reason (first_seen and attempts
// kept), and a creation among those keeps blocking its own subtree. A
// dependent that stays blocked is relabeled when its nearest held creation
// changed. Every clock hold and dependent that stays has an attempt
// counted. Permanent holds, and their subtrees, stay. It returns the held
// creations for the push's batches.
func releasePushHolds(ctx context.Context, stderr io.Writer, store *sqlite.Store) (*blockers, error) {
	holds, err := store.HeldPushEvents(ctx, -1)
	if err != nil {
		return nil, err
	}
	if len(holds) == 0 {
		return newBlockers(), nil
	}
	st, err := newSettlement(ctx, store, holds)
	if err != nil {
		return nil, err
	}
	p, blocked, err := st.plan(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.apply(ctx, store); err != nil {
		return nil, err
	}
	for _, h := range holds {
		if why, ok := p.released[h.EventID]; ok {
			fmt.Fprintf(stderr, "push: released held event %s (%s %s): %s\n",
				oneLine(h.EventID), oneLine(h.NodeID), oneLine(h.OpType), why)
		}
	}
	return blocked, nil
}

// settlement is what releasePushHolds knows about the holds of one push:
// the holds in queue order with their checks, and the verdict of each event
// validated so far ("" when it passes the hub's rules at the store's clock,
// else the reason it would be held for).
type settlement struct {
	store   *sqlite.Store
	holds   []sqlite.HeldPushEvent
	checks  []holdCheck
	verdict map[string]string
}

// newSettlement reads what settling holds needs: the verdicts of the clock
// holds.
func newSettlement(ctx context.Context, store *sqlite.Store, holds []sqlite.HeldPushEvent) (*settlement, error) {
	st := &settlement{store: store, holds: holds, checks: make([]holdCheck, len(holds)), verdict: map[string]string{}}
	var clock []string
	for i, h := range holds {
		st.checks[i] = heldCheck(h)
		if strings.HasPrefix(h.Reason, holdClockPrefix) {
			clock = append(clock, h.EventID)
		}
	}
	if err := st.validate(ctx, clock); err != nil {
		return nil, err
	}
	return st, nil
}

// validate gives each of ids without a verdict one, reading their pending
// events in one query. An id with no pending event passes: releasing its
// hold sends nothing.
func (st *settlement) validate(ctx context.Context, ids []string) error {
	var need []string
	for _, id := range ids {
		if _, ok := st.verdict[id]; !ok {
			need = append(need, id)
			st.verdict[id] = ""
		}
	}
	events, err := readPendingEventsByID(ctx, st.store, need)
	if err != nil {
		return err
	}
	now := st.store.Now()
	for _, e := range events {
		if refused := transport.ValidatePushEvent(e, now); refused != nil {
			st.verdict[e.EventID] = pushHoldReason(e, refused)
		}
	}
	return nil
}

// plan works out the releases and relabels of the push and the held
// creations left. The dependents a pass would release are validated; if
// any fails, the pass runs again with it kept. A failure only keeps more
// events held, so the second pass releases nothing new and needs no more
// validation.
func (st *settlement) plan(ctx context.Context) (*holdPlan, *blockers, error) {
	failed := map[string]bool{}
	for {
		p, blocked := st.pass(failed)
		if err := st.validate(ctx, p.candidates); err != nil {
			return nil, nil, err
		}
		more := false
		for _, id := range p.candidates {
			if st.verdict[id] != "" {
				failed[id], more = true, true
			}
		}
		if !more {
			return p, blocked, nil
		}
	}
}

// pass plans one settlement: clock holds that passed are released, then
// each dependent in queue order is kept under its own reason (failed),
// relabeled or kept while a held creation blocks it (blockers.holdFor) or
// the creation its reason names is still held (blockers.heldFor), or
// released. On this
// replica a task's creation precedes every event of its subtree in Lamport
// order, so a creation released here stops blocking before its subtree is
// checked.
func (st *settlement) pass(failed map[string]bool) (*holdPlan, *blockers) {
	blocked := buildBlockers(st.holds)
	p := &holdPlan{released: map[string]string{}, relabel: map[string]string{}}
	for _, h := range st.holds {
		if v, ok := st.verdict[h.EventID]; ok && v == "" && strings.HasPrefix(h.Reason, holdClockPrefix) {
			p.release(h, "its stamp is now within 24h of this machine's clock", blocked)
		}
	}
	for i, d := range st.holds {
		if !strings.HasPrefix(d.Reason, holdDependsPrefix) {
			continue
		}
		if failed[d.EventID] {
			p.relabel[d.EventID] = st.verdict[d.EventID]
			continue
		}
		_, why, ok := blocked.holdFor(st.checks[i])
		if !ok && blocked.heldFor(d.Reason) {
			why, ok = d.Reason, true
		}
		switch {
		case !ok:
			p.release(d, "no held creation it waits for any more", blocked)
			p.candidates = append(p.candidates, d.EventID)
		case why != d.Reason:
			p.relabel[d.EventID] = why
		}
	}
	return p, blocked
}

// holdPlan collects what releasePushHolds decides before any of it is
// written: holds to release (with why), holds to relabel, and the released
// dependents, which are validated before the plan is kept.
type holdPlan struct {
	released   map[string]string
	relabel    map[string]string
	candidates []string
}

// release plans to release the hold h; a released creation stops blocking.
func (p *holdPlan) release(h sqlite.HeldPushEvent, why string, blocked *blockers) {
	p.released[h.EventID] = why
	if h.OpType == string(model.OpCreateNode) {
		blocked.remove(h.EventID)
	}
}

// apply writes the plan: released holds; one attempt for every clock hold
// and dependent left, which this push checked and kept; then relabels,
// which keep first_seen and attempts.
func (p *holdPlan) apply(ctx context.Context, store *sqlite.Store) error {
	ids := make([]string, 0, len(p.released))
	for id := range p.released {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if err := store.ReleasePushHolds(ctx, ids); err != nil {
		return err
	}
	if err := store.NotePushHoldAttempts(ctx, []string{holdClockPrefix, holdDependsPrefix}); err != nil {
		return err
	}
	return store.SetPushHoldReasons(ctx, p.relabel)
}

// readPendingEventsByID returns the pending events among ids, read from
// sync_events in one query, in Lamport order.
func readPendingEventsByID(ctx context.Context, store *sqlite.Store, ids []string) ([]*model.SyncEvent, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("read held events: %w", err)
	}
	// The pending events whose ids are in the bound JSON array.
	rows, err := store.Query(ctx, `
		SELECT event_id, project_prefix, node_id, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash
		FROM sync_events
		WHERE sync_status = 'pending' AND event_id IN (SELECT value FROM json_each(?))
		ORDER BY lamport_clock ASC`, string(list))
	if err != nil {
		return nil, fmt.Errorf("read held events: %w", err)
	}
	return scanSyncEvents(rows, len(ids))
}

// heldIndex keeps the held creations of one push current (MTIX-95.12,
// pre-reviews of round 5). They are filed under their tasks' current
// numbers, which a renumber changes: one the hub asks for in this push
// (pushBatch moves the subtree; stale), or one committed by another
// connection or process while the push runs. version is the store's data
// version read BEFORE the index's holds were read, so a commit that lands
// while they are read, or while the release step waits for the write lock,
// shows as a change. Each batch is decided and then the version is read
// again, after the batch's tasks were read (holdBatch): if it changed, the
// index is read again and the batch decided again, until the version holds
// still, so the index and the tasks a decision uses are read at the same
// data version.
type heldIndex struct {
	blocked *blockers
	version int64
	stale   bool
}

// maxIndexReads bounds how often one batch reads the index again because
// other connections keep committing.
const maxIndexReads = 16

// readIndex notes the store's data version, then reads the held creations
// with read: in this order, a commit made while they are read shows as a
// change of version afterwards.
func readIndex(ctx context.Context, store *sqlite.Store, read func() (*blockers, error),
) (*blockers, int64, error) {
	version, err := store.DataVersion(ctx)
	if err != nil {
		return nil, 0, err
	}
	blocked, err := read()
	if err != nil {
		return nil, 0, err
	}
	return blocked, version, nil
}

// newHeldIndex settles the push holds (releasePushHolds), whose held
// creations become the index, read after the data version (readIndex).
func newHeldIndex(ctx context.Context, stderr io.Writer, store *sqlite.Store) (*heldIndex, error) {
	blocked, version, err := readIndex(ctx, store, func() (*blockers, error) {
		return releasePushHolds(ctx, stderr, store)
	})
	if err != nil {
		return nil, err
	}
	return &heldIndex{blocked: blocked, version: version}, nil
}

// reload reads the held creations again, after the data version
// (readIndex).
func (h *heldIndex) reload(ctx context.Context, store *sqlite.Store) error {
	blocked, version, err := readIndex(ctx, store, func() (*blockers, error) {
		holds, err := store.HeldPushEvents(ctx, -1)
		if err != nil {
			return nil, err
		}
		return buildBlockers(holds), nil
	})
	if err != nil {
		return err
	}
	h.blocked, h.version, h.stale = blocked, version, false
	return nil
}

// holdBatch decides the batch events against the index (decideBatch),
// reading the index again first when a renumber in this push made it stale,
// and again, with the batch decided anew, whenever the data version read
// after the decision differs from the index's. Once a decision stands it
// records its holds (recordBatch) and returns the events to send and how
// many it newly held.
func (h *heldIndex) holdBatch(ctx context.Context, stderr io.Writer, store *sqlite.Store,
	events []*model.SyncEvent,
) ([]*model.SyncEvent, int, error) {
	for reads := 0; reads < maxIndexReads; reads++ {
		if h.stale {
			if err := h.reload(ctx, store); err != nil {
				return nil, 0, err
			}
		}
		d, err := decideBatch(ctx, store, events, h.blocked)
		if err != nil {
			return nil, 0, err
		}
		version, err := store.DataVersion(ctx)
		if err != nil {
			return nil, 0, err
		}
		if version == h.version {
			return recordBatch(ctx, stderr, store, d)
		}
		h.stale = true
	}
	return nil, 0, fmt.Errorf("the store kept changing while push read its held creations; push again")
}
