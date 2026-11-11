// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// BoardExclusion is one part of an export that a clone of the hub may not
// reproduce, including when replaying events from older clients, and so is left
// out of the enrolment's comparison (MTIX-112.17.16.6). Path is dotted from the
// export's top level; a segment that holds a list applies to every element.
// Pushed-board differences are demonstrated by
// TestEnrolWorktree_RealPath_ExclusionsEachHideARealDifference.
// The sessions and dependencies.created_at exclusions have no pushed-board
// proof; dependency timestamp differences depend on clock timing. A field not
// listed here is always compared.
type BoardExclusion struct {
	Path   string
	Reason string
}

// boardStamps are the export's own stamps: computed from the export when it is
// written, so they differ whenever anything else does and say nothing more.
const (
	stampExportedAt  = "exported_at"
	stampChecksum    = "checksum"
	stampIDsChecksum = "ids_checksum"
	// stampNodeCount is derived from the nodes, which are compared row by row.
	stampNodeCount = "node_count"
)

// BoardExclusions is the complete, documented exclusion set.
//
// The rule is what a field can hide: metadata a clone stamps for itself, and
// host-local registrations, are left out; user content never is. Deletions,
// defer wake times and every other field stay differences, so a cascade, an
// undelete or a defer that did not replicate refuses and the report names it.
func BoardExclusions() []BoardExclusion {
	return []BoardExclusion{
		{"nodes.activity", "the activity log never replicates; it is metadata, not board content"},
		{"nodes.updated_at", "a local write stamps updated_at from a clock reading apart from its event's time, and some local writes move it with no event (the auto-block when a dependency is added, removing a dependency), so a clone derives it from the latest applied event's time and it can differ; created_at is the create event's time and still compares"},
		{"dependencies.created_at", "apply stamps the clone's own clock on an edge it creates; the edge itself (from, to, type) still compares; it differs only when the two clocks read different seconds"},
		{"nodes.annotations.id", "for events from before MTIX-112.17.16.15, a locally written comment keeps its local id while a clone uses the event id; comments still compare by author, text and order"},
		{"nodes.annotations.created_at", "for events from before MTIX-112.17.16.15, a clone takes the event time rather than the annotation's stored creation time, with its own clock as fallback if the event time is out of range; author, text and order still compare"},
		{"agents", "host-local agent registrations, not board content; the aside store keeps them"},
		{"sessions", "host-local session registrations, not board content; the aside store keeps them"},
	}
}

// BoardPresenceOnly lists fields whose value is a clock reading a clone stamps
// for itself, but whose presence is board content: only whether the field is
// set is compared. A deletion the clone also holds therefore matches, while a
// deletion on one side only (a cascade or an undelete that did not replicate)
// is still a difference.
func BoardPresenceOnly() []BoardExclusion {
	return []BoardExclusion{
		{"nodes.deleted_at", "apply stamps the deleting replica's own clock; whether the node is deleted is compared, not when"},
	}
}

// BoardDiff maps an id to the names of the fields on which the two sides differ.
type BoardDiff map[string][]string

// IDs returns the sorted ids.
func (d BoardDiff) IDs() []string {
	out := make([]string, 0, len(d))
	for id := range d {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (d BoardDiff) add(id, field string) {
	for _, f := range d[id] {
		if f == field {
			return
		}
	}
	d[id] = append(d[id], field)
	sort.Strings(d[id])
}

// diffKind says which side holds a differing row or field.
type diffKind int

const (
	// kindChanged: both sides hold the row or section and the value differs.
	kindChanged diffKind = iota
	// kindStoreOnly: only the first (local) side holds the row.
	kindStoreOnly
	// kindHubOnly: only the second (hub clone) side holds the row.
	kindHubOnly
)

// boardCmp collects a comparison: the diff, which side each difference is on,
// and a hash of the values on both sides (never the values themselves).
type boardCmp struct {
	// replicas is set when the two sides are a store and a hub clone: two
	// replicas, so labels spacing alone is not a difference. The board file
	// and its store are one replica, and a raw comparison compares nothing away.
	replicas bool
	// evidence is what is known of how the clone got its values; nil when none.
	evidence *CloneEvidence
	diff     BoardDiff
	kinds    map[string]diffKind
	vals     map[string][]string
	// rowKinds keeps every kind a whole-row difference had under one key: a
	// store-only and a hub-only dependency from one node share the key, and
	// merging them to "changed" would hide the store-only one.
	rowKinds map[string]map[diffKind]bool
	// superset marks a list-valued field whose first side is wholly contained
	// in the second: the second side only holds more.
	superset map[string]bool
}

func newBoardCmp() *boardCmp {
	return &boardCmp{diff: BoardDiff{}, kinds: map[string]diffKind{}, vals: map[string][]string{},
		rowKinds: map[string]map[diffKind]bool{}, superset: map[string]bool{}}
}

// add records one difference. a and b are the two sides' values (nil when a
// side has none); only their hash is kept.
func (c *boardCmp) add(id, field string, kind diffKind, a, b any) {
	c.diff.add(id, field)
	key := id + "\x00" + field
	origKind := kind
	if old, ok := c.kinds[key]; ok && old != kind {
		kind = kindChanged
	}
	c.kinds[key] = kind
	if c.rowKinds[key] == nil {
		c.rowKinds[key] = map[diffKind]bool{}
	}
	c.rowKinds[key][origKind] = true
	x, _ := json.Marshal(a) //nolint:errchkjson // a decoded JSON value always encodes; the hash only needs stable bytes
	y, _ := json.Marshal(b) //nolint:errchkjson // as above
	sum := sha256.Sum256(append(append(x, 0x02), y...))
	// Several rows can share one id and field (dependencies from one node):
	// each row's hash is kept and the hashes are combined in sorted order, so
	// neither map order nor an overwrite decides the digest.
	c.vals[key] = append(c.vals[key], hex.EncodeToString(sum[:]))
}

// without returns the comparison minus the differences of one kind.
func (c *boardCmp) without(drop diffKind) *boardCmp {
	out := newBoardCmp()
	for id, fields := range c.diff {
		for _, f := range fields {
			key := id + "\x00" + f
			if c.kinds[key] == drop {
				continue
			}
			out.diff.add(id, f)
			out.kinds[key], out.vals[key] = c.kinds[key], c.vals[key]
			out.rowKinds[key], out.superset[key] = c.rowKinds[key], c.superset[key]
		}
	}
	return out
}

// has reports whether any difference is of the kind.
func (c *boardCmp) has(k diffKind) bool {
	for _, v := range c.kinds {
		if v == k {
			return true
		}
	}
	return false
}

// rowsOnly lists the ids of whole rows of the given kind. Only a whole row
// (named "(node)", "(dependencies)") is ever recorded as store-only or hub-only;
// a field is always a change.
func (c *boardCmp) rowsOnly(k diffKind) []string {
	var out []string
	for id, fields := range c.diff {
		for _, f := range fields {
			if c.rowKinds[id+"\x00"+f][k] {
				out = append(out, id)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// signature is one hash over every difference and the values on both sides,
// in a fixed order. It changes whenever a differing value changes.
func (c *boardCmp) signature() string {
	keys := make([]string, 0, len(c.vals))
	for k := range c.vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		hashes := append([]string(nil), c.vals[k]...)
		sort.Strings(hashes)
		_, _ = h.Write([]byte(k + "\x03" + strings.Join(hashes, "\x04") + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CompareBoardDocs compares two exports of a board, given as their JSON, over
// everything the export carries: every section, every node field, in either
// direction. It ignores the export's own stamps and the BoardExclusions, and
// nothing else. Rows are matched by identity, never by position, so row order
// is not a difference; a repeated identity is one. It reports ids and field
// names only, never content (MTIX-112.17.16.6).
func CompareBoardDocs(a, b []byte) (BoardDiff, error) {
	c, err := compareReplicas(a, b, nil)
	if err != nil {
		return nil, err
	}
	return c.diff, nil
}

// CompareBoardDocsWith is CompareBoardDocs over a chosen exclusion list; tests
// use it to show that every exclusion hides a real difference of a pushed board.
func CompareBoardDocsWith(a, b []byte, exclusions []BoardExclusion) (BoardDiff, error) {
	c, err := compareBoards(a, b, exclusions)
	if err != nil {
		return nil, err
	}
	return c.diff, nil
}

// compareReplicas compares a store with a clone of the hub: the documented
// exclusions apply, and so do the two replication rules of sameNodeField.
func compareReplicas(a, b []byte, ev *CloneEvidence) (*boardCmp, error) {
	return compareBoardsAs(a, b, BoardExclusions(), true, ev)
}

// compareBoards compares two exports of one replica (or, with no exclusions, the
// raw documents): the replication rules of sameNodeField do not apply.
func compareBoards(a, b []byte, exclusions []BoardExclusion) (*boardCmp, error) {
	return compareBoardsAs(a, b, exclusions, false, nil)
}

func compareBoardsAs(a, b []byte, exclusions []BoardExclusion, replicas bool, ev *CloneEvidence) (*boardCmp, error) {
	da, err := decodeBoardDoc(a)
	if err != nil {
		return nil, fmt.Errorf("read the first board: %w", err)
	}
	db, err := decodeBoardDoc(b)
	if err != nil {
		return nil, fmt.Errorf("read the second board: %w", err)
	}
	for _, x := range exclusions {
		stripPath(da, strings.Split(x.Path, "."))
		stripPath(db, strings.Split(x.Path, "."))
	}
	if len(exclusions) > 0 {
		normalizeHistory(da)
		normalizeHistory(db)
		for _, x := range BoardPresenceOnly() {
			presencePath(da, strings.Split(x.Path, "."))
			presencePath(db, strings.Split(x.Path, "."))
		}
	}
	c := newBoardCmp()
	c.replicas = replicas
	c.evidence = ev
	keys := map[string]bool{}
	for k := range da {
		keys[k] = true
	}
	for k := range db {
		keys[k] = true
	}
	for k := range keys {
		switch k {
		case stampExportedAt, stampChecksum, stampIDsChecksum, stampNodeCount:
			continue
		}
		compareSection(c, k, da[k], db[k])
	}
	return c, nil
}

func decodeBoardDoc(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // numbers are compared as written, never rounded
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode: data after the board document")
	}
	if doc == nil {
		return nil, fmt.Errorf("decode: the board is not an object")
	}
	return doc, nil
}

// stripPath removes the excluded path from v; lists apply to each element.
func stripPath(v any, segs []string) {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			stripPath(e, segs)
		}
	case map[string]any:
		if len(segs) == 1 {
			delete(t, segs[0])
			return
		}
		stripPath(t[segs[0]], segs[1:])
	}
}

// presencePath replaces a set value at the path by a marker, so only whether it
// is set compares.
func presencePath(v any, segs []string) {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			presencePath(e, segs)
		}
	case map[string]any:
		if len(segs) > 1 {
			presencePath(t[segs[0]], segs[1:])
			return
		}
		if val, ok := t[segs[0]]; ok && val != nil && val != "" {
			t[segs[0]] = "(set)"
		}
	}
}

func compareSection(c *boardCmp, section string, a, b any) {
	ra, aList := asRows(a)
	rb, bList := asRows(b)
	if aList && bList {
		compareRows(c, section, ra, rb)
		return
	}
	if !sameJSON(a, b) {
		c.add("(export)", section, kindChanged, a, b)
	}
}

// asRows reads a section as a list of rows; an absent or null section is an
// empty list, as the exporter writes either.
func asRows(v any) ([]any, bool) {
	if v == nil {
		return nil, true
	}
	rows, ok := v.([]any)
	return rows, ok
}

func sameJSON(a, b any) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// rowIdentity returns the key a row is matched on and the id it is reported
// under.
func rowIdentity(section string, row any) (key, shown string) {
	m, _ := row.(map[string]any)
	str := func(k string) string { s, _ := m[k].(string); return s }
	switch section {
	case "nodes":
		return str("id"), str("id")
	case "dependencies":
		return str("from_id") + "\x00" + str("to_id") + "\x00" + str("dep_type"), str("from_id")
	case "agents":
		return str("agent_id"), "agents:" + str("agent_id")
	}
	if id := str("id"); id != "" {
		return id, section + ":" + id
	}
	// A row with no id of its own is matched on its whole content. fmt prints a
	// decoded map with its keys sorted, so equal rows share a key and no encode
	// error exists to be discarded.
	return fmt.Sprintf("%v", row), section + ":(row)"
}

func indexRows(c *boardCmp, section string, rows []any) map[string]any {
	out := make(map[string]any, len(rows))
	for _, r := range rows {
		key, shown := rowIdentity(section, r)
		if _, dup := out[key]; dup {
			c.add(shown, "(repeated)", kindChanged, r, nil)
		}
		out[key] = r
	}
	return out
}

func compareRows(c *boardCmp, section string, a, b []any) {
	ia, ib := indexRows(c, section, a), indexRows(c, section, b)
	for key, ra := range ia {
		_, shown := rowIdentity(section, ra)
		rb, ok := ib[key]
		if !ok {
			c.add(shown, rowField(section), kindStoreOnly, ra, nil)
			continue
		}
		compareFields(c, section, shown, ra, rb)
	}
	for key, rb := range ib {
		if _, ok := ia[key]; !ok {
			_, shown := rowIdentity(section, rb)
			c.add(shown, rowField(section), kindHubOnly, nil, rb)
		}
	}
}

// rowField names what differs when a whole row is on one side only.
func rowField(section string) string {
	if section == "nodes" {
		return "(node)"
	}
	return "(" + section + ")"
}

func compareFields(c *boardCmp, section, shown string, a, b any) {
	ma, _ := a.(map[string]any)
	mb, _ := b.(map[string]any)
	keys := map[string]bool{}
	for k := range ma {
		keys[k] = true
	}
	for k := range mb {
		keys[k] = true
	}
	for k := range keys {
		if sameJSON(ma[k], mb[k]) || (c.replicas && section == "nodes" && c.sameNodeField(k, shown, ma, mb)) {
			continue
		}
		name := section + "." + k
		if section == "nodes" {
			name = k
		}
		c.add(shown, name, kindChanged, ma[k], mb[k])
		if contained(ma[k], mb[k]) {
			c.superset[shown+"\x00"+name] = true
		}
	}
}

// sameNodeField reports a node field that differs in form only between replicas:
// labels in spacing, and closed_at by the one-second skew of a plain old
// terminal event (skewedLegacyClosedAt). The board file and its store, and raw
// comparisons, compare the exact value.
func (c *boardCmp) sameNodeField(k, id string, a, b map[string]any) bool {
	switch k {
	case "labels":
		return sameLabels(a[k], b[k])
	case "closed_at":
		return c.skewedLegacyClosedAt(id, a, b)
	}
	return false
}
