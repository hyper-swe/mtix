// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"time"
)

// inboxFullReadFloor is the longest a parked policy wait goes without a full
// policy read (MTIX-245). The per-tick probe only sees inbox counts, while
// eligibility depends on hub state, permissions and budget windows outside the
// inbox, so a full read still runs at least this often.
const inboxFullReadFloor = 2 * time.Second

// wallClock returns the current time with the monotonic reading stripped, so
// every comparison uses the wall clock. Go timers on darwin stop during system
// sleep; a deadline checked against this clock survives it (MTIX-245).
func wallClock() time.Time { return time.Now().Round(0) }

// WithWaitClock injects the wall clock the inbox wait measures its timeout and
// full-read floor against; nil restores the system wall clock (MTIX-245).
func (s *InboxService) WithWaitClock(now func() time.Time) *InboxService {
	s.waitClock = now
	return s
}

func (s *InboxService) wallNow() time.Time {
	if s.waitClock != nil {
		return s.waitClock()
	}
	return wallClock()
}

// wallDeadline is entry plus timeout, or the zero time for an unbounded wait.
func wallDeadline(entry time.Time, timeout time.Duration) time.Time {
	if timeout <= 0 {
		return time.Time{}
	}
	return entry.Add(timeout)
}

// waitProbe is the cheap per-tick view of an inbox: its counts and when the
// last full read was started.
type waitProbe struct {
	pending, total int64
	at             time.Time
	ok             bool
}

// probeInbox takes the counts BEFORE the full read, so an event landing between
// the two shows as a change on the next tick. A backend without counts, or a
// failed count, yields a probe that never matches, which forces a full read.
func (s *InboxService) probeInbox(ctx context.Context, agent string) waitProbe {
	p := waitProbe{at: s.wallNow()}
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

// parkUntilChange sleeps tick by tick until a full read is due: the probe
// changed or the floor passed. It reports done=true with a nil error when the
// wall-clock deadline passed (the wait's own timeout), or with the caller's
// cancellation as the error (MTIX-245, FR-19.4).
func (s *InboxService) parkUntilChange(ctx, waitCtx context.Context, tick <-chan time.Time, agent string, last waitProbe, deadline time.Time) (bool, error) {
	for {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return true, fmt.Errorf("inbox wait interrupted: %w", ctx.Err())
			}
			return true, nil
		case <-tick:
		}
		now := s.wallNow()
		if !deadline.IsZero() && !now.Before(deadline) {
			return true, nil // wall-clock timeout, also after a system sleep
		}
		if now.Sub(last.at) >= inboxFullReadFloor || !last.ok {
			return false, nil
		}
		cur := s.probeInbox(waitCtx, agent)
		if !cur.ok || cur.pending != last.pending || cur.total != last.total {
			return false, nil
		}
	}
}
