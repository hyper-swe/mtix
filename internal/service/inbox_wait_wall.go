// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Full-read floors of a parked wait (MTIX-245, MTIX-326). While events are
// pending (held by the wake policy) or the backend has no change token,
// eligibility can move with nothing committed to the board database: operator
// local grants and denies, budget windows and hub state. Those waits keep the
// old 2 s full read. Only an empty inbox on a backend with a token waits for
// inboxSafetyFloor, because nothing could be woken by an eligibility change
// and a new event always commits.
const (
	inboxFullReadFloor = 2 * time.Second
	inboxSafetyFloor   = time.Minute
)

// wallClock returns the current time with the monotonic reading stripped, so
// every comparison uses the wall clock. Go timers on darwin stop during system
// sleep; a deadline checked against this clock survives it (MTIX-245).
func wallClock() time.Time { return time.Now().Round(0) }

// WithWaitSafetyFloor sets the longest an unchanged wait goes without a full
// read; non-positive restores inboxSafetyFloor (MTIX-326).
func (s *InboxService) WithWaitSafetyFloor(d time.Duration) *InboxService {
	s.waitFloor = d
	return s
}

// WithWaitClock injects the wall clock the inbox wait measures its timeout
// against; nil restores the system wall clock (MTIX-245).
func (s *InboxService) WithWaitClock(now func() time.Time) *InboxService {
	s.waitClock = now
	return s
}

// WithWaitMonotonic injects the monotonic clock the inbox wait measures its
// timeout and full-read floor against; nil restores time.Now, whose readings
// carry the monotonic component (MTIX-245).
func (s *InboxService) WithWaitMonotonic(now func() time.Time) *InboxService {
	s.waitMono = now
	return s
}

func (s *InboxService) wallNow() time.Time {
	if s.waitClock != nil {
		return s.waitClock()
	}
	return wallClock()
}

func (s *InboxService) monoNow() time.Time {
	if s.waitMono != nil {
		return s.waitMono()
	}
	return time.Now()
}

// waitWindow is the timeout of one wait, anchored on both clocks at entry. It
// is spent when EITHER clock has advanced by the timeout: the wall clock covers
// system sleep (monotonic time pauses), the monotonic clock covers a wall
// clock stepped backwards (MTIX-245). The zero timeout never expires.
type waitWindow struct {
	wall, mono time.Time
	timeout    time.Duration
}

func (s *InboxService) openWaitWindow(timeout time.Duration) waitWindow {
	return waitWindow{wall: s.wallNow(), mono: s.monoNow(), timeout: timeout}
}

func (w waitWindow) spent(wall, mono time.Time) bool {
	if w.timeout <= 0 {
		return false
	}
	return mono.Sub(w.mono) >= w.timeout || wall.Sub(w.wall) >= w.timeout
}

// waitProbe is the cheap per-tick view of an inbox: its counts, the database
// change token and when the last full read was started, on the monotonic
// clock (MTIX-326).
type waitProbe struct {
	pending, total int64
	at             time.Time
	ok             bool
	tok            string
	tokOK          bool
}

// waitState is the bookkeeping of one parked wait: whether a token failure was
// already reported, and when a read last carried the session touch (the lease).
type waitState struct {
	warned    bool
	touchedAt time.Time
}

// changeToken reads the store's change token; a backend without one, or a
// failed read, reports false and the caller counts every tick instead. A
// failure on a live context is logged once per wait, so a persistent stat
// error (for example a permission problem) is visible to the operator.
func (s *InboxService) changeToken(ctx context.Context, ws *waitState) (string, bool) {
	if s.changes == nil {
		return "", false
	}
	tok, err := s.changes.InboxChangeToken(ctx)
	if err == nil {
		return tok, true
	}
	if !ws.warned && ctx.Err() == nil {
		ws.warned = true
		lg := s.logger
		if lg == nil {
			lg = slog.Default()
		}
		lg.Warn("inbox change token unavailable; the wait counts every tick", slog.Any("error", err))
	}
	return "", false
}

// parkedReadCtx is the context of a re-read while parked. A re-read is the
// wait's own bookkeeping, not the agent reading and not a session sign of
// life, so it carries no session touch and no read stamp: each would commit to
// the board database and move every waiter's change token, which is what the
// park avoids (MTIX-326). The lease is kept by one touching read at most every
// sqlite.InboxReadThrottle; the entry read always touches.
func (s *InboxService) parkedReadCtx(waitCtx context.Context, ws *waitState) context.Context {
	now := s.monoNow()
	if now.Sub(ws.touchedAt) >= sqlite.InboxReadThrottle {
		ws.touchedAt = now
		return waitCtx
	}
	return sqlite.WithoutReadStamp(sqlite.WithSessionTouch(waitCtx, sqlite.SessionTouch{}))
}

// probeInbox takes the token and then the counts BEFORE the full read, so an
// event landing between the two shows as a change on the next tick. A backend
// without counts, or a failed count, yields a probe that never matches, which
// forces a full read.
func (s *InboxService) probeInbox(ctx context.Context, agent string, ws *waitState) waitProbe {
	p := waitProbe{at: s.monoNow()}
	p.tok, p.tokOK = s.changeToken(ctx, ws)
	return s.countInto(ctx, agent, p)
}

func (s *InboxService) countInto(ctx context.Context, agent string, p waitProbe) waitProbe {
	if s.counter == nil {
		return p
	}
	pending, total, err := s.counter.InboxCount(ctx, agent)
	if err != nil {
		return p
	}
	p.pending, p.total, p.ok = pending, total, true
	return p
}

func (s *InboxService) safetyFloor() time.Duration {
	if s.waitFloor > 0 {
		return s.waitFloor
	}
	return inboxSafetyFloor
}

// readFloor is how long a parked wait may go without a full read. Held events
// or a missing token keep the 2 s floor of MTIX-245 (eligibility can move
// outside the database); an empty inbox with a token waits for the safety
// floor, since only a commit can add an event and the token announces it.
func (s *InboxService) readFloor(last waitProbe) time.Duration {
	if last.pending > 0 || !last.tokOK {
		return inboxFullReadFloor
	}
	return s.safetyFloor()
}

// readDue reports whether the parked wait must run a full read now.
func (s *InboxService) readDue(last waitProbe, mono time.Time) bool {
	return !last.ok || mono.Sub(last.at) >= s.readFloor(last)
}

// parkUntilChange sleeps tick by tick until a full read is due. While the
// change token is unchanged a tick asks the database nothing; a moved token
// runs one count, and a moved count ends the park (MTIX-326). A commit that
// moves no count (another agent's write) costs one count and no full read. It
// reports done=true with a nil error when the wait's own timeout passed on
// either clock, or with the caller's cancellation as the error (MTIX-245,
// FR-19.4).
func (s *InboxService) parkUntilChange(ctx, waitCtx context.Context, tick <-chan time.Time, agent string, last waitProbe, win waitWindow, ws *waitState) (bool, error) {
	for {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return true, fmt.Errorf("inbox wait interrupted: %w", ctx.Err())
			}
			return true, nil
		case <-tick:
		}
		mono := s.monoNow()
		if win.spent(s.wallNow(), mono) {
			return true, nil // timeout on either clock, also after a system sleep
		}
		if s.readDue(last, mono) {
			return false, nil
		}
		tok, tokOK := s.changeToken(waitCtx, ws)
		if tokOK && last.tokOK && tok == last.tok {
			continue // nothing was committed: no query at all
		}
		cur := s.countInto(waitCtx, agent, waitProbe{tok: tok, tokOK: tokOK})
		if !cur.ok || cur.pending != last.pending || cur.total != last.total {
			return false, nil
		}
		last.tok, last.tokOK = tok, tokOK
	}
}
