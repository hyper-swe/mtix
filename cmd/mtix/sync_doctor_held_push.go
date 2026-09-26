// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// heldPushCheckName is the name of the doctor's held-push-events check.
const heldPushCheckName = "held push events"

// heldPushListed is how many held push events the check's detail names.
const heldPushListed = 5

// heldPushReasonRunes caps each listed reason, in runes.
const heldPushReasonRunes = 200

// appendHeldPushCheck adds the doctor's "held push events" check
// (MTIX-95.12). It is local only and fails like the other local checks when
// there is no local store.
func appendHeldPushCheck(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	if st == nil {
		return appendCheck(r, heldPushCheckName, false, "local store not initialized")
	}
	ok, detail := checkHeldPushEvents(ctx, st)
	return appendCheck(r, heldPushCheckName, ok, detail)
}

// checkHeldPushEvents passes when push holds no event. It fails while any
// is held: those local changes have not reached the hub, so teammates do not
// see them. The detail gives the count and names the first few held events,
// each with the fix that matches its op and why it is held
// (heldPushGuidance) and its reason, then the read-only command that lists
// them all.
func checkHeldPushEvents(ctx context.Context, st *sqlite.Store) (bool, string) {
	n, err := st.CountHeldPushEvents(ctx)
	if err != nil {
		return false, err.Error()
	}
	if n == 0 {
		return true, "ok"
	}
	held, err := st.HeldPushEvents(ctx, heldPushListed)
	if err != nil {
		return false, err.Error()
	}
	items := make([]string, 0, len(held)+1)
	for _, h := range held {
		reason := []rune(oneLine(h.Reason))
		if len(reason) > heldPushReasonRunes {
			reason = append(reason[:heldPushReasonRunes], []rune("...")...)
		}
		items = append(items, fmt.Sprintf("%s %s: %s (reason: %s)",
			oneLine(h.NodeID), oneLine(h.OpType), heldPushGuidance(h), string(reason)))
	}
	if n > len(held) {
		items = append(items, fmt.Sprintf("and %d more", n-len(held)))
	}
	return false, fmt.Sprintf("%d push events held, not pushed: %s. List them all: %s (source push in --json)",
		n, strings.Join(items, "; "), quarantineInspectCmd)
}

// heldPushGuidance is the fix doctor gives for one held push event, from
// its op and the kind of its reason (MTIX-95.12): a link or unlink made
// while a creation is held waits until no creation made before it is held;
// any other dependent resolves with the creation it waits for; a clock hold waits for its stamp to be
// within 24 h of this machine's clock; a held task creation cannot be fixed
// by an edit, so the task must be escalated; a field over the limit is
// shortened or split, and the next edit pushes.
func heldPushGuidance(h sqlite.HeldPushEvent) string {
	switch {
	case strings.HasPrefix(h.Reason, holdDependsPrefix) && strings.Contains(h.Reason, holdLinkWait):
		return "a link made while a task creation is held; it pushes once no creation made before it is held " +
			"(this version names a link's target by number)"
	case strings.HasPrefix(h.Reason, holdDependsPrefix):
		return "resolves with the held creation it depends on"
	case strings.HasPrefix(h.Reason, holdClockPrefix):
		return "check this machine's clock; the event pushes once its stamp is within 24 h of the clock"
	case h.OpType == string(model.OpCreateNode):
		return "stop editing this task and escalate: its creation cannot be sent, and there is no automatic re-send"
	case strings.HasPrefix(h.Reason, holdTooLargePrefix):
		return "shorten or split the field; the next edit pushes"
	default:
		return "the hub refuses this event; show the reason to the operator"
	}
}
