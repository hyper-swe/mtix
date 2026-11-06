// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"sort"
	"strings"
)

// recordHeld keeps, for each whole row the bounded clone lacks but the full hub
// clone holds, the fields in which the store and the full clone differ, and a
// hash of the values on both sides. The row advice names those fields, the
// report lists them, and the digest follows their values, so a held row never
// hides a changed field (MTIX-112.17.16.18, ruling r3 point 2). c compares the
// local export with the full clone.
func (f *BoardFinding) recordHeld(c *boardCmp) {
	f.heldFields = map[string][]string{}
	var sigs []string
	for _, id := range f.StoreOnlyRows() {
		if f.hubLacks[id+"\x00(node)"] {
			continue
		}
		for _, field := range c.diff[id] {
			if strings.HasPrefix(field, "(") {
				continue
			}
			f.heldFields[id] = append(f.heldFields[id], field)
			sigs = append(sigs, id+"\x00"+field+"\x00"+strings.Join(c.vals[id+"\x00"+field], ","))
		}
		sort.Strings(f.heldFields[id])
	}
	sort.Strings(sigs)
	f.heldSig = strings.Join(sigs, "\n")
}

// HeldFields maps each node the hub already holds (though the bounded clone
// lacked it) to the fields in which this store differs from the hub; empty when
// the held rows carry the same values. Ids and field names only.
func (f BoardFinding) HeldFields() map[string][]string {
	out := map[string][]string{}
	for id, fields := range f.heldFields {
		out[id] = append([]string(nil), fields...)
	}
	for id, fields := range out {
		if len(fields) == 0 {
			delete(out, id)
		}
	}
	return out
}

// heldAdvice words the rows the hub already holds, naming the fields that
// differ on each so the operator sees what step (3) would drop.
func (f BoardFinding) heldAdvice(ids []string) string {
	var named, same []string
	for _, id := range ids {
		if fields := f.heldFields[id]; len(fields) > 0 {
			named = append(named, id+" ("+strings.Join(fields, ", ")+")")
		} else {
			same = append(same, id)
		}
	}
	out := "the hub already holds " + idList(ids) + ": do not create or add them again, that would duplicate them"
	if len(named) > 0 {
		out += "; the value differs there in " + strings.Join(namedFields(named), "; ") + ", and step (3) gives you the hub's value"
	}
	if len(same) > 0 && len(named) > 0 {
		out += "; " + idList(same) + " hold(s) the same values"
	} else if len(same) > 0 {
		out += " (with the same values; step (3) gives you the hub's copy)"
	}
	return out
}

// namedFields reshapes "ID (a, b)" entries into "ID: a, b" lines.
func namedFields(named []string) []string {
	out := make([]string, len(named))
	for i, n := range named {
		out[i] = strings.Replace(strings.TrimSuffix(n, ")"), " (", ": ", 1)
	}
	return out
}
