// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"log/slog"
	"time"
)

// inboxWaitSafetyFloor is the longest an idle InboxWait goes without a query
// while the change token is unchanged (MTIX-328, as inboxSafetyFloor in the
// service). A commit that leaves the stat token unchanged is caught by it.
const inboxWaitSafetyFloor = time.Minute

// waitPark is the bookkeeping of one parked InboxWait: when a read last
// carried the session touch (the lease) and whether a token failure was
// already reported.
type waitPark struct {
	touchedAt time.Time
	warned    bool
}

// waitToken reads the change token; a failed read reports false and the caller
// reads every tick instead. A failure on a live context is logged once.
func (s *Store) waitToken(ctx context.Context, p *waitPark) (string, bool) {
	tok, err := s.InboxChangeToken(ctx)
	if err == nil {
		return tok, true
	}
	if !p.warned && ctx.Err() == nil {
		p.warned = true
		s.logger.Warn("inbox change token unavailable; the wait reads every tick", slog.Any("error", err))
	}
	return "", false
}

// waitReadCtx is the context of a re-read while parked. A re-read is the wait's
// bookkeeping, not a session sign of life: it carries no session touch, so it
// commits nothing and does not move the change token of every other waiter.
// The lease is kept by one touching read at most every InboxReadThrottle.
func (s *Store) waitReadCtx(ctx context.Context, p *waitPark) context.Context {
	now := s.waitMonoNow()
	if now.Sub(p.touchedAt) >= InboxReadThrottle {
		p.touchedAt = now
		return ctx
	}
	return context.WithValue(ctx, sessionTouchKey{}, SessionTouch{})
}

// waitSafety is the safety-read floor; a test may shorten it.
func (s *Store) waitSafety() time.Duration {
	if s.waitFloor > 0 {
		return s.waitFloor
	}
	return inboxWaitSafetyFloor
}

// parkUntilMoved sleeps tick by tick until the token differs from tok or the
// safety floor since readAt has passed. While the token is unchanged a tick
// runs no query. It returns true when the wait's own window is spent.
func (s *Store) parkUntilMoved(ctx context.Context, tick <-chan time.Time, win waitWindow, p *waitPark, tok string, tokOK bool, readAt time.Time) (spent bool, err error) {
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-tick:
		}
		mono := s.waitMonoNow()
		if win.spent(s.waitNow(), mono) {
			return true, nil // timeout on either clock, also after a system sleep (MTIX-245)
		}
		if !tokOK || mono.Sub(readAt) >= s.waitSafety() {
			return false, nil
		}
		if cur, ok := s.waitToken(ctx, p); !ok || cur != tok {
			return false, nil
		}
	}
}
