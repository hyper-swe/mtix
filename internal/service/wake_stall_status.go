// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// stalledEvents derives the stalled state of the agent's addressed, unacknowledged events
// (MTIX-112.40.12). Stalled means no hook waiter is armed and T = wake.confirm_window has
// passed since the event, or since its push was recorded. Nothing is written and no timer
// runs; the report is computed each time it is read, so it is noticed at the next sender
// status read, doctor run or dispatcher pass. It returns nil when the readers are absent.
func (s *WakeStatusService) stalledEvents(ctx context.Context, agent string) (map[int64]WakeStatus, error) {
	if s.deps.Pending == nil || s.deps.WaiterLive == nil {
		return nil, nil
	}
	live, err := s.deps.WaiterLive(ctx, agent)
	if err != nil || live {
		return nil, wrapStall("waiter", err)
	}
	pending, err := s.deps.Pending(ctx, agent)
	if err != nil || len(pending) == 0 {
		return nil, wrapStall("pending events", err)
	}
	_, window, err := s.deps.Settings(agent)
	if err != nil {
		return nil, wrapStall("settings", err)
	}
	rows, err := s.deps.Rows(ctx, agent, 1000)
	if err != nil {
		return nil, wrapStall("ledger", err)
	}
	bySeq := make(map[int64]model.WakeStatusRow, len(rows))
	for _, r := range rows {
		bySeq[r.EventSeq] = r
	}
	return s.judgeStalled(agent, pending, bySeq, window), nil
}

func wrapStall(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("read %s for stalled state: %w", what, err)
}

// judgeStalled applies the stall rule to each pending event against its ledger row, if any.
func (s *WakeStatusService) judgeStalled(agent string, pending []PendingEvent, rows map[int64]model.WakeStatusRow, window time.Duration) map[int64]WakeStatus {
	now := s.deps.Clock()
	out := map[int64]WakeStatus{}
	for _, p := range pending {
		in := StallInput{Now: now, Window: window, EventAt: p.At}
		since := p.At
		if r, ok := rows[p.Seq]; ok {
			in.HasRow, in.Outcome = true, r.Outcome
			at, err := time.Parse(time.RFC3339Nano, firstNonEmptyTime(r.OutcomeAt, r.ClaimedAt))
			if err != nil {
				continue // an unreadable time is left to the row's own status, never guessed
			}
			in.RowAt, since = at, at
		}
		if !EventStalled(in) {
			continue
		}
		st := WakeStatus{State: model.WakeStateStalled, Reason: model.WakeReasonWaiterNotArmed, EventSeq: p.Seq,
			Since: since.UTC().Format(time.RFC3339Nano), AgeSeconds: int64(now.Sub(since) / time.Second)}
		if r, ok := rows[p.Seq]; ok {
			st.Route = r.Route
		}
		st.Fix = statusFix(st.State, agent)
		out[p.Seq] = st
	}
	return out
}

func firstNonEmptyTime(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// firstStalled returns the oldest stalled event of the agent, if any.
func (s *WakeStatusService) firstStalled(ctx context.Context, agent string) (WakeStatus, bool, error) {
	stalled, err := s.stalledEvents(ctx, agent)
	if err != nil || len(stalled) == 0 {
		return WakeStatus{}, false, err
	}
	lowest := int64(0)
	for seq := range stalled {
		if lowest == 0 || seq < lowest {
			lowest = seq
		}
	}
	return stalled[lowest], true, nil
}

// mergeStalled adds the stalled events that have no ledger row of their own, newest first,
// and trims to limit.
func mergeStalled(out []WakeStatus, extra map[int64]WakeStatus, limit int) []WakeStatus {
	if len(extra) == 0 {
		return out
	}
	for _, st := range extra {
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EventSeq > out[j].EventSeq })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
