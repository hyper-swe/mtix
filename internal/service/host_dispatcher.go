// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// HostDispatcherOptions provides protected routing and per-checkout opening, never a global DSN.
type HostDispatcherOptions struct {
	State     *oplocal.State
	OpenBoard func(context.Context, oplocal.ListedBoard) (*HostBoard, error)
}

// HostDispatcher scans listed boards and retains each checkout's contact cadence.
type HostDispatcher struct {
	opts      HostDispatcherOptions
	schedules map[string]*ContactScheduler
}

// NewHostDispatcher validates its host-local dependencies (FR-20.4).
func NewHostDispatcher(o HostDispatcherOptions) (*HostDispatcher, error) {
	if o.State == nil || o.OpenBoard == nil {
		return nil, fmt.Errorf("host dispatcher capabilities required: %w", model.ErrInvalidInput)
	}
	return &HostDispatcher{opts: o, schedules: map[string]*ContactScheduler{}}, nil
}

// Pass reloads listings and membership; one refused checkout does not block another.
func (d *HostDispatcher) Pass(ctx context.Context) error {
	registry, err := d.opts.State.DispatchBoards(ctx)
	if err != nil {
		return err
	}
	var failures error
	active := map[string]bool{}
	for _, row := range registry.Listed {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		if row.StoreInstanceID == "" {
			continue
		} // legacy routing is not a verified store-instance match
		board, err := d.opts.OpenBoard(ctx, row)
		if err == nil && board == nil {
			err = fmt.Errorf("board opener returned no checkout: %w", model.ErrInvalidInput)
		}
		if err == nil {
			key := row.Path + "\x00" + row.StoreInstanceID
			active[key] = true
			if sched := d.schedules[key]; sched != nil {
				board.opts.Contact = sched
			} else if board.opts.Contact != nil {
				d.schedules[key] = board.opts.Contact
			}
			err = board.pass(ctx, row)
		}
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("host checkout %q: %w", row.Path, err))
		}
	}
	for key := range d.schedules {
		if !active[key] {
			delete(d.schedules, key)
		}
	}
	return failures
}

// inboxOnlyAdapter has no live route; later harness integrations supply real adapters.
type inboxOnlyAdapter struct{}

func (inboxOnlyAdapter) Route() WakeRoute                            { return RouteInboxOnly }
func (inboxOnlyAdapter) Capabilities(model.WakeKey) WakeCapabilities { return WakeCapabilities{} }
func (inboxOnlyAdapter) Probe(context.Context, model.WakeKey) WakeHealth {
	return WakeHealth{Detail: "live harness route unavailable; use mtix inbox"}
}
func (inboxOnlyAdapter) Contract(context.Context) ContractCheck {
	return ContractCheck{Pinned: "inbox-only", Matches: true}
}
func (inboxOnlyAdapter) Deliver(context.Context, model.WakeAttempt) (model.WakeResult, error) {
	return model.WakeResult{Outcome: model.WakeUndeliverable, Route: string(RouteInboxOnly), Reason: "no-live-route"}, nil
}
