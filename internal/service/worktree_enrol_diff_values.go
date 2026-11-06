// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"sort"
	"strings"
)

// sameLabels compares two labels values as lists. A local write keeps the text
// it was given and a clone keeps the text the hub returned, so the same labels
// can differ in spacing only (review F4).
func sameLabels(a, b any) bool {
	x, okA := labelList(a)
	y, okB := labelList(b)
	if !okA || !okB || len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// labelList reads a labels value: a JSON array text, or a comma separated text.
func labelList(v any) ([]string, bool) {
	s, ok := v.(string)
	if !ok {
		return nil, false
	}
	var list []string
	if json.Unmarshal([]byte(strings.TrimSpace(s)), &list) != nil {
		list = strings.Split(s, ",")
	}
	out := make([]string, 0, len(list))
	for _, l := range list {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out, true
}

// contained reports whether a is a list (or absent) whose every element is in
// the list b, one for one: b only holds more.
func contained(a, b any) bool {
	la, okA := a.([]any)
	lb, okB := b.([]any)
	if a == nil {
		okA = true
	}
	if !okA || !okB {
		return false
	}
	have := map[string]int{}
	for _, e := range lb {
		j, _ := json.Marshal(e) //nolint:errchkjson // a decoded JSON value always encodes
		have[string(j)]++
	}
	for _, e := range la {
		j, _ := json.Marshal(e) //nolint:errchkjson // as above
		if have[string(j)] == 0 {
			return false
		}
		have[string(j)]--
	}
	return true
}

// historyDropKeys lists the keys of a history row that a clone writes for
// itself: the row's own id, its clock reading, the replica that wrote it, the
// event it came from, and the actor (a comment applied from an event carries
// the event's author where the local write carried the command's actor, seen
// in a real clone). The row's task, kind, field and old and new values are
// kept. It is a function, not a package variable, so no state is shared.
func historyDropKeys() []string {
	return []string{"id", "ts", "origin", "event_id", "actor"}
}

// historyClockFields names the history fields whose old and new values are a
// clock reading a clone stamps for itself: the deletion time. Only whether the
// value is set compares, as nodes.deleted_at does (MTIX-112.17.16.18, ruling r4
// point 1). The match is by field name; every other part of the row is compared.
func historyClockFields() []string { return []string{"deleted_at"} }

// presenceOfHistoryValue reduces a history value to whether it is set. A value
// is a JSON text: absent, empty and null are unset.
func presenceOfHistoryValue(m map[string]any, key string) {
	v, ok := m[key]
	if !ok {
		return
	}
	if s, isStr := v.(string); v != nil && (!isStr || (s != "" && s != "null")) {
		m[key] = "(set)"
	}
}

// normalizeHistoryRow drops the keys a clone writes for itself and reduces the
// clock-valued fields of the row to presence.
func normalizeHistoryRow(m map[string]any) {
	for _, k := range historyDropKeys() {
		delete(m, k)
	}
	field, _ := m["field"].(string)
	for _, clock := range historyClockFields() {
		if field == clock {
			presenceOfHistoryValue(m, "old")
			presenceOfHistoryValue(m, "new")
		}
	}
}

// normalizeHistory reduces every history row to what a clone reproduces and
// puts each list in a fixed order, so rows compare as a multiset. A row present
// on one side only still differs (MTIX-112.17.16.6, point 4): the old rule
// dropped history whole and hid rows no hub event holds.
func normalizeHistory(doc map[string]any) {
	normalizeHistoryList(doc["history"])
	nodes, _ := doc["nodes"].([]any)
	for _, n := range nodes {
		if m, ok := n.(map[string]any); ok {
			normalizeHistoryList(m["history"])
		}
	}
}

func normalizeHistoryList(v any) {
	rows, ok := v.([]any)
	if !ok {
		return
	}
	keyed := make([]string, len(rows))
	for i, r := range rows {
		if m, isMap := r.(map[string]any); isMap {
			normalizeHistoryRow(m)
		}
		b, _ := json.Marshal(r) //nolint:errchkjson // a decoded JSON value always encodes
		keyed[i] = string(b)
	}
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool { return keyed[order[x]] < keyed[order[y]] })
	sorted := make([]any, len(rows))
	for i, o := range order {
		sorted[i] = rows[o]
	}
	copy(rows, sorted)
}
