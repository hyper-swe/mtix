// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// inboxReadPollInterval is how often a policy-applying wait probes the inbox.
const inboxReadPollInterval = 250 * time.Millisecond

func emptyInboxResult() InboxResult {
	return InboxResult{Events: []InboxEvent{}, Held: InboxHeld{Reasons: []string{}}}
}

// InboxReadWait blocks until the wake policy allows at least one unacknowledged
// event for agent or the timeout ends (ADR-007 A12, FR-19.4). The wait is
// level-triggered: every unacknowledged eligible event counts, with no
// high-water edge, so an event returned before and left unacked ends the wait
// again. An empty timeout returns the last held count, not an error. Zero
// timeout waits until an event or caller cancellation; negative is invalid.
func (s *InboxService) InboxReadWait(ctx context.Context, agent string, timeout time.Duration, opt InboxReadOptions) (InboxResult, error) {
	if s.reader == nil || timeout < 0 {
		return InboxResult{}, wrapAPIOperation("wait inbox", model.ErrInvalidInput)
	}
	waitCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if s.policy.Eligibility == nil {
		return s.waitUnpolicied(ctx, agent, timeout) // no policy: the store's own wait
	}
	ticker := time.NewTicker(inboxReadPollInterval)
	defer ticker.Stop()
	win := s.openWaitWindow(timeout)
	readCtx := ctx // the first read follows the caller, later ones the wait budget
	last := emptyInboxResult()
	for {
		probe := s.probeInbox(readCtx, agent)
		res, err := s.InboxRead(readCtx, agent, opt)
		if err != nil {
			return s.waitReadFailed(ctx, waitCtx, last, err)
		}
		readCtx, last = waitCtx, res
		if len(res.Events) > 0 || len(res.HeldEvents) > 0 {
			return res, nil
		}
		if done, werr := s.parkUntilChange(ctx, waitCtx, ticker.C, agent, probe, win); done {
			if werr != nil {
				return InboxResult{}, werr
			}
			return res, nil // the wait's own timeout: nothing eligible, held count kept
		}
	}
}

// waitReadFailed maps a failed read: caller cancellation is an error, the wait's
// own timeout hitting mid-read is an empty result that keeps the last held
// count, anything else propagates.
func (s *InboxService) waitReadFailed(ctx, waitCtx context.Context, last InboxResult, err error) (InboxResult, error) {
	if ctx.Err() != nil {
		return InboxResult{}, fmt.Errorf("inbox wait interrupted: %w", ctx.Err())
	}
	if errors.Is(err, context.DeadlineExceeded) && waitCtx.Err() != nil {
		return last, nil
	}
	return InboxResult{}, err
}

// waitUnpolicied is the wait of a service with no wake policy: the store's
// long-poll, unchanged, so callers on boards without a policy see no difference.
func (s *InboxService) waitUnpolicied(ctx context.Context, agent string, timeout time.Duration) (InboxResult, error) {
	events, err := s.InboxWait(ctx, agent, timeout)
	if err != nil {
		return InboxResult{}, err
	}
	res := emptyInboxResult()
	if events != nil {
		res.Events = events
	}
	return res, nil
}

// PolicyInboxReader adapts the policy-applying service to the narrow wait
// reader the channel source uses, so the channel path applies the same policy
// (ADR-007 A12).
type PolicyInboxReader struct{ svc *InboxService }

// NewPolicyInboxReader wraps svc.
func NewPolicyInboxReader(svc *InboxService) *PolicyInboxReader { return &PolicyInboxReader{svc: svc} }

// InboxWait returns only the events the policy allows; held events are dropped.
func (r *PolicyInboxReader) InboxWait(ctx context.Context, agent string, timeout time.Duration) ([]sqlite.InboxEvent, error) {
	res, err := r.svc.InboxReadWait(ctx, agent, timeout, InboxReadOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]sqlite.InboxEvent, len(res.Events))
	for i, e := range res.Events {
		out[i] = sqlite.InboxEvent(e)
	}
	return out, nil
}
