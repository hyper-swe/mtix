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
	r = appendCheck(r, heldPushCheckName, ok, detail)
	return appendImportRefusalCheck(r, app.syncSvc, app.mtixDir) // also local changes the hub has not seen (MTIX-112.17.16.16)
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
	all, err := st.HeldPushEvents(ctx, n)
	if err != nil {
		return false, err.Error()
	}
	relayHeld := relayHeldIDs(all)
	held := all[:min(len(all), heldPushListed)]
	items := make([]string, 0, len(held)+1)
	for _, h := range held {
		reason := []rune(displayHoldReason(h.Reason))
		if len(reason) > heldPushReasonRunes {
			reason = append(reason[:heldPushReasonRunes], []rune("...")...)
		}
		items = append(items, fmt.Sprintf("%s %s: %s (reason: %s)",
			oneLine(h.NodeID), oneLine(h.OpType), heldPushGuidance(h, relayHeld), string(reason)))
	}
	if n > len(held) {
		items = append(items, fmt.Sprintf("and %d more", n-len(held)))
	}
	return false, fmt.Sprintf("%d push events held, not pushed: %s. List them all: %s (source push in --json)",
		n, strings.Join(items, "; "), quarantineInspectCmd)
}

// dependsOnRelayHeld reports whether reason names a held relay-carried creation.
func dependsOnRelayHeld(reason string, relayHeld map[string]bool) bool {
	id, ok := heldDependencyID(reason)
	return ok && relayHeld[id]
}

// relayHeldIDs returns the event ids of the permanent holds among held: the
// relay-carried creations, and every held event whose reason names one of
// them, at any depth (a grandchild's edit names the child's creation, which
// names the relay-carried one), including a link that waits for one. It reads
// every held event, not only the listed ones.
func relayHeldIDs(held []sqlite.HeldPushEvent) map[string]bool {
	ids := make(map[string]bool)
	for _, h := range held {
		if strings.Contains(h.Reason, holdRelayCarried) {
			ids[h.EventID] = true
		}
	}
	for grown := len(ids) > 0; grown; {
		grown = false
		for _, h := range held {
			if !ids[h.EventID] && dependsOnRelayHeld(h.Reason, ids) {
				ids[h.EventID] = true
				grown = true
			}
		}
	}
	return ids
}

// heldPushGuidance is the fix doctor gives for one held push event, from
// its op and the kind of its reason (MTIX-95.12): a link or unlink made
// while a creation is held waits until no creation made before it is held;
// any other dependent resolves with the creation it waits for; a clock hold waits for its stamp to be
// within 24 h of this machine's clock; a held task creation cannot be fixed
// by an edit, so the task must be escalated; a field over the limit is
// shortened or split, and the next edit pushes. A relay-carried creation, and
// whatever depends on it (relayHeld holds the event ids of the held relay
// creations), is held for good and is escalated.
func heldPushGuidance(h sqlite.HeldPushEvent, relayHeld map[string]bool) string {
	switch {
	case relayHeld[h.EventID] && strings.Contains(h.Reason, holdLinkWait):
		return relayLinkGuidance
	case strings.HasPrefix(h.Reason, holdDependsPrefix) && strings.Contains(h.Reason, holdLinkWait):
		return "a link made while a task creation is held; it pushes once no creation made before it is held " +
			"(this version names a link's target by number)"
	case relayHeld[h.EventID]:
		return relayHeldGuidance
	case strings.HasPrefix(h.Reason, holdDependsPrefix):
		return "resolves with the held creation it depends on"
	case strings.HasPrefix(h.Reason, holdClockPrefix):
		return "check this machine's clock; the event pushes once its stamp is within 24 h of the clock"
	case strings.Contains(h.Reason, holdRelayCarried):
		return relayHeldGuidance
	case h.OpType == string(model.OpCreateNode):
		return "stop editing this task and escalate: its creation cannot be sent, and there is no automatic re-send"
	case strings.HasPrefix(h.Reason, holdTooLargePrefix):
		return "shorten or split the field; the next edit pushes"
	default:
		return "the hub refuses this event; show the reason to the operator"
	}
}

// relayHeldGuidance is the text for a relay-carried creation the hub would
// renumber, and for what depends on it. The hold is permanent: relay-carried
// tasks are never renumbered, and no command in this build clears it, so the
// operator escalates. The hub's task holding that number does not reach
// relay-only peers while it stands.
const relayHeldGuidance = "stop editing this task and its subtree and escalate: relay-carried tasks are never renumbered, " +
	"so this hold is permanent, pushing more edits to it or its subtree only adds held events, " +
	"and the hub's task holding that number does not reach relay-only peers"

// relayLinkGuidance is the text for a link or unlink that waits for a
// relay-carried creation: every dependency link made after the hold waits for
// it, and the hold is permanent, so the link is never pushed.
const relayLinkGuidance = "a dependency link made after a relay-carried creation was held; relay-carried tasks " +
	"are never renumbered, so this hold is permanent and the link is never pushed while it stands: " +
	"escalate, and expect every later dependency link to be held the same way"
