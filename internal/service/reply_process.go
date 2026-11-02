// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// replyScanBatch bounds one journal read of the reply scan.
const replyScanBatch = 100

// maxReplyWindows bounds the open sender windows kept per board.
const maxReplyWindows = 64

// ReplyJournal is the local journal reads the reply scan needs.
type ReplyJournal interface {
	ReplyByEvents(ctx context.Context, afterSeq int64, limit int) ([]sqlite.ReplyByEvent, error)
	ReplyArrived(ctx context.Context, from, to string, afterSeq int64) (bool, error)
	ListRegistrations(ctx context.Context, hostID, uid string) ([]sqlite.Registration, error)
}

// ReplyProcessor keeps the operator-local reply accounting of one board
// (MTIX-112.20.3). It reads only the local journal and never contacts the hub;
// the host dispatcher calls Tick every pass and Scan after an enrolled pull.
type ReplyProcessor struct {
	Journal  ReplyJournal
	State    *oplocal.State
	Key      string
	Host     RegistrationHost
	Settings ReplySettings
	Clock    func() time.Time
}

// Tick accrues remote-held time, ends holds past their deadline or the daily
// cap, and drops sender windows past their TTL.
func (p *ReplyProcessor) Tick(ctx context.Context) error {
	pol, err := ReadReplyPolicy(p.Settings)
	if err != nil {
		return err
	}
	now := p.Clock().UTC()
	cur, err := p.State.ReadReplyState(p.Key)
	if err != nil {
		return fmt.Errorf("reply tick: %w", err)
	}
	if !settleDue(cur, now) {
		return nil // nothing to settle: an idle board writes nothing
	}
	return p.State.UpdateReplyState(ctx, p.Key, func(st *oplocal.ReplyState) error {
		settleReplyState(st, pol, now)
		return nil
	})
}

// replyAccrualEvery bounds how often active holds are re-accounted, so a
// held board does not rewrite its record on every local tick.
const replyAccrualEvery = 15 * time.Second

// settleDue reports whether a tick has anything to write: a hold or window
// past its deadline, extension history to prune, or held time to accrue.
func settleDue(st oplocal.ReplyState, now time.Time) bool {
	for _, times := range st.Extensions {
		if len(pruneExtensions(times, now)) != len(times) {
			return true
		}
	}
	for _, w := range st.Windows {
		if !now.Before(w.ReplyBy) {
			return true
		}
	}
	for _, h := range st.Holds {
		if !now.Before(h.Until) {
			return true
		}
	}
	return len(st.Holds) > 0 && now.Sub(st.AccountedTo) >= replyAccrualEvery
}

func settleReplyState(st *oplocal.ReplyState, pol ReplyPolicy, now time.Time) {
	accrueReplyHeld(st, pol, now)
	holds := st.Holds[:0:0]
	for _, h := range st.Holds {
		if now.Before(h.Until) {
			holds = append(holds, h)
		}
	}
	st.Holds = holds
	windows := st.Windows[:0:0]
	for _, w := range st.Windows {
		if now.Before(w.ReplyBy) {
			windows = append(windows, w)
		}
	}
	st.Windows = windows
	for sender, times := range st.Extensions {
		kept := pruneExtensions(times, now)
		if len(kept) == 0 {
			delete(st.Extensions, sender)
		} else {
			st.Extensions[sender] = kept
		}
	}
}

func pruneExtensions(times []time.Time, now time.Time) []time.Time {
	var kept []time.Time
	for _, t := range times {
		if now.Sub(t) < replyExtensionWindow && !t.After(now) {
			kept = append(kept, t)
		}
	}
	return kept
}

// Scan examines journal events that arrived since the last scan: it closes
// sender windows whose awaited reply arrived, and turns events that carry
// reply_by, are addressed to an agent registered on this host and were pulled
// from the hub into holds (C2: any sender). The decision follows the event's
// origin, not its author: an event written on this machine needs no hub
// contact so it never holds, and a pulled event whose author id equals a local
// agent id still holds.
func (p *ReplyProcessor) Scan(ctx context.Context) error {
	pol, err := ReadReplyPolicy(p.Settings)
	if err != nil {
		return err
	}
	st, err := p.State.ReadReplyState(p.Key)
	if err != nil {
		return fmt.Errorf("reply scan: %w", err)
	}
	events, err := p.collect(ctx, st.Cursor)
	if err != nil {
		return err
	}
	answered, err := p.answered(ctx, st.Windows)
	if err != nil {
		return err
	}
	registered, err := p.registered(ctx, len(events) > 0)
	if err != nil {
		return err
	}
	now := p.Clock().UTC()
	return p.State.UpdateReplyState(ctx, p.Key, func(cur *oplocal.ReplyState) error {
		settleReplyState(cur, pol, now)
		closeAnswered(cur, answered)
		for _, ev := range events {
			// The cursor is re-read under the lock: a concurrent scan that
			// already examined this event moved it past, so apply it once.
			if ev.Seq <= cur.Cursor {
				continue
			}
			cur.Cursor = ev.Seq
			applyReplyEvent(cur, pol, registered, ev, now)
		}
		return nil
	})
}

func (p *ReplyProcessor) collect(ctx context.Context, cursor int64) ([]sqlite.ReplyByEvent, error) {
	var all []sqlite.ReplyByEvent
	for {
		batch, err := p.Journal.ReplyByEvents(ctx, cursor, replyScanBatch)
		if err != nil {
			return nil, fmt.Errorf("reply scan: %w", err)
		}
		all = append(all, batch...)
		if len(batch) < replyScanBatch {
			return all, nil
		}
		cursor = batch[len(batch)-1].Seq
	}
}

func (p *ReplyProcessor) answered(ctx context.Context, windows []oplocal.ReplyWindow) (map[string]bool, error) {
	out := map[string]bool{}
	for _, w := range windows {
		ok, err := p.Journal.ReplyArrived(ctx, w.AwaitingFrom, w.Sender, w.OpenedSeq)
		if err != nil {
			return nil, fmt.Errorf("reply scan: %w", err)
		}
		if ok {
			out[windowKey(w)] = true
		}
	}
	return out, nil
}

func (p *ReplyProcessor) registered(ctx context.Context, needed bool) (map[string]bool, error) {
	out := map[string]bool{}
	if !needed {
		return out, nil
	}
	regs, err := p.Journal.ListRegistrations(ctx, p.Host.HostID, p.Host.UID)
	if err != nil {
		return nil, fmt.Errorf("reply scan registrations: %w", err)
	}
	for _, r := range regs {
		out[r.AgentID] = true
	}
	return out, nil
}

func closeAnswered(st *oplocal.ReplyState, answered map[string]bool) {
	kept := st.Windows[:0:0]
	for _, w := range st.Windows {
		if !answered[windowKey(w)] {
			kept = append(kept, w)
		}
	}
	st.Windows = kept
}

// applyReplyEvent turns one journal event into a hold when every rule holds:
// the event was pulled from the hub (one this machine wrote itself needs no hub
// contact, whoever its author), To is registered on this host and is not the
// author (an agent cannot hold on its own message), the deadline is in the
// future, the sender is within the extension limit and the day's cap is not
// spent. The hold lasts until min(reply_by, now + 8h).
func applyReplyEvent(st *oplocal.ReplyState, pol ReplyPolicy, registered map[string]bool, ev sqlite.ReplyByEvent, now time.Time) {
	if ev.Local || !registered[ev.To] || ev.Author == ev.To {
		return
	}
	replyBy := parseReplyBy(ev.ReplyBy)
	until := ClampHold(replyBy, now)
	if replyBy.IsZero() || !until.After(now) || capSpent(*st, pol, now) {
		return
	}
	times := pruneExtensions(st.Extensions[ev.Author], now)
	if pol.ExtensionsPerHour > 0 && int64(len(times)) >= pol.ExtensionsPerHour {
		st.Extensions = setExtensions(st.Extensions, ev.Author, times)
		return
	}
	st.Extensions = setExtensions(st.Extensions, ev.Author, append(times, now))
	st.Holds = append(st.Holds, oplocal.ReplyHold{Sender: ev.Author, EventID: ev.EventID, Start: now, Until: until})
}

func setExtensions(m map[string][]time.Time, sender string, times []time.Time) map[string][]time.Time {
	if m == nil {
		m = map[string][]time.Time{}
	}
	m[sender] = times
	return m
}

// OpenReplyWindow records a sender window: the sender keeps 60 s pulls until
// the awaited agent answers or ReplyBy. A second window to the same agent on
// the same node replaces the first.
func OpenReplyWindow(ctx context.Context, state *oplocal.State, key string, w oplocal.ReplyWindow) error {
	return state.UpdateReplyState(ctx, key, func(st *oplocal.ReplyState) error {
		kept := make([]oplocal.ReplyWindow, 0, len(st.Windows)+1)
		for _, old := range st.Windows {
			if old.AwaitingFrom == w.AwaitingFrom && old.Node == w.Node && old.Sender == w.Sender {
				continue
			}
			if w.OpenedAt.Before(old.ReplyBy) {
				kept = append(kept, old)
			}
		}
		kept = append(kept, w)
		if len(kept) > maxReplyWindows {
			kept = kept[len(kept)-maxReplyWindows:]
		}
		st.Windows = kept
		return nil
	})
}

// windowKey identifies a sender window by value, independent of time zones.
func windowKey(w oplocal.ReplyWindow) string {
	return fmt.Sprintf("%s|%s|%s|%d", w.AwaitingFrom, w.Sender, w.Node, w.OpenedSeq)
}
