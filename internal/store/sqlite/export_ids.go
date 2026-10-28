// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/hyper-swe/mtix/internal/model"
)

// Ids that are not valid UTF-8 (MTIX-187.7.15.1). JSON cannot carry such a
// string, so the export file spells it twice: the plain field keeps what a
// v0.5.4 reader can read, and an optional hex field carries the exact bytes.
// The hex fields are written only for a value that is not valid UTF-8, so an
// export without one is byte-identical to the format before this change.
//
//   - node: uid_hex; the plain uid is left out, so a v0.5.4 reader gives that
//     one node a fresh uid instead of adopting a changed identity.
//   - history entry: task_uid_hex and event_id_hex; the plain task_uid and
//     event_id keep JSON's U+FFFD spelling (a v0.5.4 reader rejects an empty
//     task_uid).
//
// The checksum covers the view a v0.5.4 reader has (readerView: no hex
// fields, an invalid uid left out); ids_checksum covers the hex fields.

// exportNodePlain is exportNode without its JSON methods.
type exportNodePlain exportNode

// exportHistoryPlain is exportHistoryEntry without its JSON methods.
type exportHistoryPlain exportHistoryEntry

type exportNodeWire struct {
	exportNodePlain
	UIDHex string `json:"uid_hex,omitempty"`
}

type exportHistoryWire struct {
	exportHistoryPlain
	TaskUIDHex string `json:"task_uid_hex,omitempty"`
	EventIDHex string `json:"event_id_hex,omitempty"`
}

// MarshalJSON writes the node, with uid_hex in place of an invalid-UTF-8 uid
// (a node in readerView never writes the hex field; a plainUID node keeps the
// raw uid, which encoding/json spells with \ufffd escapes, as 0.5.3 wrote it).
func (n exportNode) MarshalJSON() ([]byte, error) {
	w := exportNodeWire{exportNodePlain: exportNodePlain(n)}
	if !utf8.ValidString(n.UID) && !n.plainUID {
		w.UID = ""
		if !n.readerView {
			w.UIDHex = hex.EncodeToString([]byte(n.UID))
		}
	}
	return json.Marshal(w)
}

// UnmarshalJSON reads the node; uid_hex, when present, wins over uid. A hex
// field that is not hex is model.ErrInvalidInput.
func (n *exportNode) UnmarshalJSON(data []byte) error {
	w := exportNodeWire{exportNodePlain: exportNodePlain(*n)}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	if w.UIDHex != "" {
		uid, err := decodeIDHex("uid_hex", w.UIDHex)
		if err != nil {
			return err
		}
		w.UID = uid
	}
	*n = exportNode(w.exportNodePlain)
	return nil
}

// MarshalJSON writes the entry, with task_uid_hex and event_id_hex beside the
// U+FFFD spelling of an invalid-UTF-8 value (none in readerView).
func (e exportHistoryEntry) MarshalJSON() ([]byte, error) {
	w := exportHistoryWire{exportHistoryPlain: exportHistoryPlain(e)}
	if !e.readerView {
		if !utf8.ValidString(e.TaskUID) {
			w.TaskUIDHex = hex.EncodeToString([]byte(e.TaskUID))
		}
		if !utf8.ValidString(e.EventID) {
			w.EventIDHex = hex.EncodeToString([]byte(e.EventID))
		}
	}
	return json.Marshal(w)
}

// UnmarshalJSON reads the entry; a hex field wins over its plain field.
func (e *exportHistoryEntry) UnmarshalJSON(data []byte) error {
	w := exportHistoryWire{exportHistoryPlain: exportHistoryPlain(*e)}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	if w.TaskUIDHex != "" {
		v, err := decodeIDHex("task_uid_hex", w.TaskUIDHex)
		if err != nil {
			return err
		}
		w.TaskUID = v
	}
	if w.EventIDHex != "" {
		v, err := decodeIDHex("event_id_hex", w.EventIDHex)
		if err != nil {
			return err
		}
		w.EventID = v
	}
	*e = exportHistoryEntry(w.exportHistoryPlain)
	return nil
}

func decodeIDHex(field, value string) (string, error) {
	raw, err := hex.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("%s is not hex: %w", field, model.ErrInvalidInput)
	}
	return string(raw), nil
}

// readerViewNodes returns copies of nodes (history included) that marshal as
// a v0.5.4 reader sees them: no hex field, an invalid uid left out.
func readerViewNodes(nodes []exportNode) []exportNode {
	if nodes == nil {
		return nil // nil encodes as null, [] as []: the checksum tells them apart
	}
	out := make([]exportNode, len(nodes))
	for i := range nodes {
		out[i] = nodes[i]
		out[i].readerView = true
		out[i].History = readerViewHistory(nodes[i].History)
	}
	return out
}

// readerViewHistory is readerViewNodes for a list of history entries.
func readerViewHistory(history []exportHistoryEntry) []exportHistoryEntry {
	if history == nil {
		return nil
	}
	out := make([]exportHistoryEntry, len(history))
	for i := range history {
		out[i] = history[i]
		out[i].readerView = true
	}
	return out
}

// idTuple names one hex-carrying value: what it is, whose it is, its bytes.
type idTuple struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
	Hex  string `json:"hex"`
}

// computeIDsChecksum returns the SHA-256 of the sorted tuples of every value
// that is not valid UTF-8 (node uid by node id; history task_uid and
// event_id by entry id), or "" when there is none. Keys are spelled as JSON
// spells them, so the writer's bytes and a reader's decoded text agree.
func computeIDsChecksum(nodes []exportNode, history []exportHistoryEntry) (string, error) {
	var tuples []idTuple
	add := func(kind, key, value string) {
		if !utf8.ValidString(value) {
			tuples = append(tuples, idTuple{kind, jsonSpelling(key), hex.EncodeToString([]byte(value))})
		}
	}
	entries := func(list []exportHistoryEntry) {
		for i := range list {
			add("task_uid", list[i].ID, list[i].TaskUID)
			add("event_id", list[i].ID, list[i].EventID)
		}
	}
	for i := range nodes {
		add("uid", nodes[i].ID, nodes[i].UID)
		entries(nodes[i].History)
	}
	entries(history)
	if len(tuples) == 0 {
		return "", nil
	}
	sort.Slice(tuples, func(i, j int) bool {
		a, b := tuples[i], tuples[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Hex < b.Hex
	})
	canonical, err := json.Marshal(tuples)
	if err != nil {
		return "", fmt.Errorf("encode ids for checksum: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(canonical)), nil
}

// jsonSpelling returns s as a JSON round trip leaves it (each invalid byte
// becomes U+FFFD).
func jsonSpelling(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	var out string
	if json.Unmarshal(b, &out) != nil {
		return s
	}
	return out
}
