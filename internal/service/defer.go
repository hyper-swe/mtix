// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// ParseDeferUntil parses a defer wake time given at an API boundary: the CLI
// --until flag, the MCP until argument or the HTTP until field (FR-3.8b,
// MTIX-95.22). An empty string means no wake time and returns nil. Any other
// value must be an RFC 3339 timestamp with a time and a zone, such as
// 2026-04-01T00:00:00Z, whose UTC year is 1..9999; it is returned in UTC.
//
// Returns ErrInvalidInput for a value that is not RFC 3339, or whose UTC year
// is outside 1..9999 (model.IsStorableTime): "9999-12-31T23:00:00-05:00" is
// valid RFC 3339 but is year 10000 in UTC, text the store could not read back.
func ParseDeferUntil(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf(
			"invalid until %q: want an RFC 3339 timestamp such as 2026-04-01T00:00:00Z: %w",
			raw, model.ErrInvalidInput)
	}
	if !model.IsStorableTime(parsed) {
		return nil, fmt.Errorf("invalid until %q: its UTC year must be 1 to 9999: %w",
			raw, model.ErrInvalidInput)
	}
	utc := parsed.UTC()
	return &utc, nil
}

// DeferNode defers a node and stores its wake time in the same transaction
// (FR-3.8, FR-3.8b, MTIX-95.22). Every defer path calls it: the CLI, MCP,
// HTTP and gRPC. until is stored in UTC, whole seconds; nil stores no wake
// time and clears an earlier one. A node deferred with a wake time is
// reopened by the next background pass once that time has passed, and claims
// are refused until then (FR-10.4).
//
// author is the caller's identity: the process author for the CLI (MTIX-24),
// the agent or header identity elsewhere. It is recorded on the activity
// entry and the sync event. The sync event is the 0.5.x transition_status
// event; the wake time does not travel to peers.
//
// Returns ErrInvalidInput if until's UTC year is outside 1..9999 (checked
// here too because the gRPC path passes a time.Time and runs no parser),
// ErrInvalidTransition if the node cannot be deferred from its current
// status, and ErrNotFound if it does not exist.
func (svc *NodeService) DeferNode(
	ctx context.Context, id string, until *time.Time, reason, author string,
) error {
	if until != nil && !model.IsStorableTime(*until) {
		return fmt.Errorf("defer %s: until's UTC year must be 1 to 9999: %w", id, model.ErrInvalidInput)
	}
	node, err := svc.store.GetNode(ctx, id)
	if err != nil {
		return fmt.Errorf("get node for defer: %w", err)
	}
	if err := model.ValidateTransition(node.Status, model.StatusDeferred); err != nil {
		return err
	}

	var wake *time.Time
	if until != nil {
		utc := until.UTC().Truncate(time.Second)
		wake = &utc
	}
	if err := svc.store.DeferNode(ctx, id, wake, reason, author); err != nil {
		return fmt.Errorf("defer %s: %w", id, err)
	}

	svc.broadcastEvent(ctx, EventStatusChanged, id, author, nil)
	return nil
}
