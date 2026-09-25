// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// The subtree rule of held task creations (MTIX-95.12; review r1 S0, r5
// S1/S2, pre-review of round 5).
//
// In 0.5.x a pushed event names its task by display number only, and a
// teammate may create a task with the same number while this replica's
// creation is not on the hub. So, while a task's create_node is held, push
// holds every event about that task or any task of its subtree. Which task
// an event is about is decided by its uid, not by the number it names: the
// event is held when the node whose uid is the event's uid is the held
// creation's node or lies below it in the local tree now (soft-deleted
// nodes count). A local renumber (an import merge, a settle, a renumber the
// hub asks for) moves a whole subtree, so an event made under an earlier
// number still finds its task, and a task that later takes the old number
// is not held by mistake. An event of a task whose own creation is held is
// held by its uid alone, even when mtix gc purged the task's node. Any
// other event without a uid, or whose uid no node has any more, is checked
// by the number it names instead, against the number each held creation's
// event names and its current number.
//
// A dependency link or unlink names the task it points to by number only,
// and a local renumber records no history of numbers, so what a link's
// number meant cannot be told afterwards. While any task creation is held,
// every link or unlink made after the earliest held creation, in queue
// order, is held (holdLinkWait), naming that creation; it is released once
// no creation made before it is held. A held event also stays held while
// the creation its reason names is held (heldFor).

// holdLinkWait ends the reason of a link or unlink that waits for the
// earliest held creation.
const holdLinkWait = "links made while a task creation is held wait for it; " +
	"this version names a link's target by number"

// blocker is a held local task creation: its event, the number that event
// names and its place in the queue, and the hold reasons of the events it
// blocks once worked out.
type blocker struct {
	eventID, nodeID, uid string
	lamport              int64
	why, linkWhy         string
}

// before reports whether b comes before the event (lamport, eventID) in
// queue order.
func (b *blocker) before(lamport int64, eventID string) bool {
	return b.lamport < lamport || (b.lamport == lamport && b.eventID < eventID)
}

// blockerKey is one place a blocker is filed: a key of byTask or byNumber.
type blockerKey struct {
	byTask bool
	key    string
}

// blockers are the held creations of one push. byTask files each under the
// current number of its task, for events found by uid; byNumber under the
// number its event names and its current number, for the others; byUID
// under its task's uid, for the events of that task itself, whether or not
// the task still has a node. keys is the reverse index remove uses; byEvent
// finds a creation by its event id; first is the earliest in queue order,
// worked out again after it is removed.
type blockers struct {
	byTask, byNumber map[string][]*blocker
	keys             map[string][]blockerKey
	byEvent, byUID   map[string]*blocker
	first            *blocker
	firstStale       bool
}

// newBlockers returns an empty set of held creations.
func newBlockers() *blockers {
	return &blockers{byTask: map[string][]*blocker{}, byNumber: map[string][]*blocker{},
		keys: map[string][]blockerKey{}, byEvent: map[string]*blocker{}, byUID: map[string]*blocker{}}
}

// empty reports whether no held creation blocks anything.
func (bs *blockers) empty() bool {
	return len(bs.byEvent) == 0
}

// add files b under currentID, the current number of its task (empty when
// the task has no node), and under the numbers for events not found by uid.
func (bs *blockers) add(b *blocker, currentID string) {
	file := func(byTask bool, key string) {
		if key == "" {
			return
		}
		m := bs.byNumber
		if byTask {
			m = bs.byTask
		}
		m[key] = append(m[key], b)
		bs.keys[b.eventID] = append(bs.keys[b.eventID], blockerKey{byTask: byTask, key: key})
	}
	bs.byEvent[b.eventID] = b
	if b.uid != "" {
		bs.byUID[b.uid] = b
	}
	if !bs.firstStale && (bs.first == nil || b.before(bs.first.lamport, bs.first.eventID)) {
		bs.first = b
	}
	file(true, currentID)
	file(false, b.nodeID)
	if currentID != b.nodeID {
		file(false, currentID)
	}
}

// remove forgets the held creation eventID, whose hold ended, under the
// keys it was filed under only.
func (bs *blockers) remove(eventID string) {
	for _, k := range bs.keys[eventID] {
		m := bs.byNumber
		if k.byTask {
			m = bs.byTask
		}
		kept := m[k.key][:0]
		for _, b := range m[k.key] {
			if b.eventID != eventID {
				kept = append(kept, b)
			}
		}
		if len(kept) == 0 {
			delete(m, k.key)
			continue
		}
		m[k.key] = kept
	}
	delete(bs.keys, eventID)
	if b, ok := bs.byEvent[eventID]; ok && bs.byUID[b.uid] == b {
		delete(bs.byUID, b.uid)
	}
	delete(bs.byEvent, eventID)
	if bs.first != nil && bs.first.eventID == eventID {
		bs.first, bs.firstStale = nil, true
	}
}

// earliest returns the held creation that comes first in queue order, or
// nil when none is held.
func (bs *blockers) earliest() *blocker {
	if bs.firstStale {
		bs.first, bs.firstStale = nil, false
		for _, b := range bs.byEvent {
			if bs.first == nil || b.before(bs.first.lamport, bs.first.eventID) {
				bs.first = b
			}
		}
	}
	return bs.first
}

// holdCheck is what the rule needs to know about one event: its id, the
// number it names, its op, its place in the queue and the task it is about.
type holdCheck struct {
	eventID, nodeID string
	op              model.OpType
	lamport         int64
	subject         sqlite.PushSubject
}

// heldCheck is the holdCheck of a held push event.
func heldCheck(h sqlite.HeldPushEvent) holdCheck {
	return holdCheck{eventID: h.EventID, nodeID: h.NodeID, op: model.OpType(h.OpType),
		lamport: h.Lamport, subject: h.PushSubject}
}

// holdFor returns the held creation that blocks c and the reason to hold
// c for: for a link or unlink, the earliest held creation when it comes
// before c in queue order; for any other event, the creation of the task
// it is about when that is held, else the deepest held creation at or
// above that task, found by uid, or by the numbers c names when its uid is
// empty or no node has it. An event's own creation never blocks it.
func (bs *blockers) holdFor(c holdCheck) (*blocker, string, bool) {
	if c.op == model.OpLinkDep || c.op == model.OpUnlinkDep {
		b := bs.earliest()
		if b == nil || !b.before(c.lamport, c.eventID) {
			return nil, "", false
		}
		why := b.linkReason()
		return b, why, true
	}
	if b, ok := bs.byUID[c.subject.UID]; ok && c.subject.UID != "" && b.eventID != c.eventID {
		why := b.reason()
		return b, why, true
	}
	byUID := c.subject.UID != "" && c.subject.CurrentNodeID != ""
	line, m := nodeLine(c.nodeID), bs.byNumber
	if byUID {
		line, m = nodeLine(c.subject.CurrentNodeID), bs.byTask
	}
	for _, n := range line {
		for _, b := range m[n] {
			if b.eventID != c.eventID {
				why := b.reason()
				return b, why, true
			}
		}
	}
	return nil, "", false
}

// heldFor reports whether the creation a dependent's reason names is still
// held. A dependent stays held while it is (MTIX-95.12): no local change
// moves a task out of a subtree, but mtix gc can purge a deleted task and
// with it the tree the rule reads, and a renumber can leave numbers no held
// creation's event names; the first finding then stands.
func (bs *blockers) heldFor(reason string) bool {
	rest, ok := strings.CutPrefix(reason, holdDependsPrefix)
	if !ok {
		return false
	}
	eventID, _, _ := strings.Cut(rest, " ")
	_, held := bs.byEvent[eventID]
	return held
}

// reason is the hold reason of an event of b's subtree, worked out once: a
// push can check thousands of events against one held creation.
func (b *blocker) reason() string {
	if b.why == "" {
		b.why = quarantineReason(fmt.Errorf("%s%s (%s)", holdDependsPrefix, b.eventID, b.nodeID))
	}
	return b.why
}

// linkReason is the hold reason of a link or unlink that waits for b, the
// earliest held creation, worked out once.
func (b *blocker) linkReason() string {
	if b.linkWhy == "" {
		b.linkWhy = quarantineReason(fmt.Errorf("%s%s (%s): %s", holdDependsPrefix, b.eventID, b.nodeID, holdLinkWait))
	}
	return b.linkWhy
}

// nodeLine returns nodeID and its ancestors, deepest first: the dot-path
// prefixes of the display number ("P-1.2.3", "P-1.2", "P-1"). Empty for "".
func nodeLine(nodeID string) []string {
	var line []string
	for id := nodeID; id != ""; {
		line = append(line, id)
		i := strings.LastIndex(id, ".")
		if i < 0 {
			break
		}
		id = id[:i]
	}
	return line
}

// buildBlockers returns the held task creations among holds, each filed
// under the current number of its task and the numbers for events not
// found by uid.
func buildBlockers(holds []sqlite.HeldPushEvent) *blockers {
	bs := newBlockers()
	for _, h := range holds {
		if h.OpType != string(model.OpCreateNode) || h.NodeID == "" {
			continue
		}
		bs.add(&blocker{eventID: h.EventID, nodeID: h.NodeID, uid: h.UID, lamport: h.Lamport}, h.CurrentNodeID)
	}
	return bs
}
