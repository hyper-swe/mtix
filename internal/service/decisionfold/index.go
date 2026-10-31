// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold

import (
	"cmp"
	"slices"
)

// index is the deduplicated view of an event set. Echoes (events of the
// same author and request key with the same payload hash) collapse to the
// member with the minimum event id; a reference to any member resolves to
// it. Members whose hashes differ are never dropped.
type index struct {
	// events holds the canonical events by event id.
	events map[string]Event
	// alias maps every event id to its canonical event id.
	alias map[string]string
	// conflicted marks canonical events whose (author, request key) group
	// holds more than one payload hash.
	conflicted map[string]bool
}

type groupKey struct{ author, key string }

func buildIndex(events []Event) *index {
	sorted := slices.Clone(events)
	slices.SortFunc(sorted, func(a, b Event) int {
		return cmp.Or(cmp.Compare(a.EventID, b.EventID), cmp.Compare(a.PayloadSHA256, b.PayloadSHA256))
	})
	// The same event id delivered twice is one event.
	sorted = slices.CompactFunc(sorted, func(a, b Event) bool { return a.EventID == b.EventID })

	idx := &index{
		events:     map[string]Event{},
		alias:      map[string]string{},
		conflicted: map[string]bool{},
	}
	type member struct{ hash, id string }
	groups := map[groupKey][]member{}
	for _, e := range sorted {
		idx.alias[e.EventID] = e.EventID
		if e.RequestKey == "" {
			continue
		}
		k := groupKey{e.AuthorID, e.RequestKey}
		groups[k] = append(groups[k], member{e.PayloadSHA256, e.EventID})
	}
	canonicalOf := map[groupKey]map[string]string{}
	for k, members := range groups {
		byHash := map[string]string{}
		for _, m := range members { // members are in event id order
			if _, ok := byHash[m.hash]; !ok {
				byHash[m.hash] = m.id
			}
		}
		canonicalOf[k] = byHash
		for _, m := range members {
			idx.alias[m.id] = byHash[m.hash]
		}
	}
	for _, e := range sorted {
		if idx.alias[e.EventID] != e.EventID {
			continue
		}
		idx.events[e.EventID] = e
		if e.RequestKey != "" && len(canonicalOf[groupKey{e.AuthorID, e.RequestKey}]) > 1 {
			idx.conflicted[e.EventID] = true
		}
	}
	return idx
}

// resolve returns the canonical event id for a reference, or the reference
// itself when no such event is known.
func (x *index) resolve(ref string) string {
	if c, ok := x.alias[ref]; ok {
		return c
	}
	return ref
}

// sortedIDs returns the canonical event ids in order.
func (x *index) sortedIDs() []string {
	ids := make([]string, 0, len(x.events))
	for id := range x.events {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
