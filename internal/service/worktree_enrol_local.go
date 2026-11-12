// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// hubIDChunk bounds how many ids one hub lookup carries.
const hubIDChunk = 500

// WorktreeLocalEventsError is the refusal listing every event the hub does not
// have a settled copy of. The recovery is in the message (ADR-007 section 4.2).
type WorktreeLocalEventsError struct {
	Events []WorktreeLocalEvent
}

// WorktreeLocalEvent is one local-only event with the reason it counts.
type WorktreeLocalEvent struct {
	sqlite.LocalEvent
	Reason string
}

// Error lists id, node, op and time of every event, then names the recovery.
func (e *WorktreeLocalEventsError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "worktree has %d local-only event(s) the hub does not hold; enrolment refused and nothing changed:", len(e.Events))
	for _, ev := range e.Events {
		fmt.Fprintf(&b, "\n  %s %s %s %s (%s)", ev.ID, ev.NodeID, ev.Op, ev.At.UTC().Format("2006-01-02T15:04:05Z"), ev.Reason)
	}
	b.WriteString("\nrecovery: push them from this worktree with its credentials (mtix sync push), or re-create the work in the enrolled checkout, then enroll again")
	return b.String()
}

// Unwrap lets callers match model.ErrConflict.
func (e *WorktreeLocalEventsError) Unwrap() error { return model.ErrConflict }

// refuseLocalOnly returns a WorktreeLocalEventsError when any event is pending,
// conflicted, or absent from the hub. An unreachable hub is an error too: the
// check never passes by default.
func (s *WorktreeEnrolment) refuseLocalOnly(ctx context.Context, root string, events []sqlite.LocalEvent) error {
	var local []WorktreeLocalEvent
	var settled []string
	byID := map[string]sqlite.LocalEvent{}
	for _, ev := range events {
		byID[ev.ID] = ev
		if ev.Status == "pending" || ev.Status == "conflicted" {
			local = append(local, WorktreeLocalEvent{LocalEvent: ev, Reason: "status " + ev.Status})
			continue
		}
		settled = append(settled, ev.ID)
	}
	for i := 0; i < len(settled); i += hubIDChunk {
		end := min(i+hubIDChunk, len(settled))
		missing, err := s.p().HubMissing(ctx, root, settled[i:end])
		if err != nil {
			return fmt.Errorf("ask the hub which events it holds: %w", err)
		}
		for _, id := range missing {
			local = append(local, WorktreeLocalEvent{LocalEvent: byID[id], Reason: "not on the hub"})
		}
	}
	if len(local) > 0 {
		return &WorktreeLocalEventsError{Events: local}
	}
	return nil
}
