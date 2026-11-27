// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/hyper-swe/mtix/internal/model"
)

// stageArray stages the elements of one array (FR-7.8, FR-7.8a). The first
// occurrence of a key is streamed one element at a time. A repeated key is
// not: encoding/json decodes it into the slice the earlier occurrence left,
// element onto element, and keeps what the new array does not overwrite (even
// the elements an earlier, shorter array cut off), so only the decoder
// itself can say what the file means. A repeated array is therefore decoded
// in memory by encoding/json (see decodeRepeated); a legitimate export never
// repeats a key, so the bounded-memory path is the only one it takes.
func (st *StagedExport) stageArray(ctx context.Context, tx *sql.Tx, dec *json.Decoder, kind stageKind) error {
	if st.seen[kind] {
		return st.decodeRepeated(ctx, tx, dec, kind)
	}
	st.seen[kind] = true
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("read %s array: %w", kind, err)
	}
	if tok == nil {
		return nil // null: a nil slice (arrayAbsent)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("export %s is not a JSON array: %w", kind, model.ErrInvalidInput)
	}
	st.presence[kind] = arrayEmpty
	for dec.More() {
		var raw json.RawMessage
		if decErr := dec.Decode(&raw); decErr != nil {
			return fmt.Errorf("decode %s element %d: %w", kind, st.counts[kind], decErr)
		}
		if rowErr := st.stageRow(ctx, tx, kind, raw); rowErr != nil {
			return fmt.Errorf("stage %s element %d: %w", kind, st.counts[kind], rowErr)
		}
	}
	if _, err = dec.Token(); err != nil { // the closing bracket
		return fmt.Errorf("read %s array end: %w", kind, err)
	}
	return nil
}

// stagedRow is one element ready to store: the text kept for hashing, its
// index keys and its non-UTF-8 id tuples (the inputs of ids_checksum).
type stagedRow struct {
	k1, k2, k3 string
	body       []byte
	ids        []idTuple
}

// stageRow validates one streamed element (it must decode as the historical
// reader decodes it) and stores its raw JSON text at the next place.
func (st *StagedExport) stageRow(ctx context.Context, tx *sql.Tx, kind stageKind, raw json.RawMessage) error {
	var row stagedRow
	switch kind {
	case stageNodes:
		var n exportNode
		if err := json.Unmarshal(raw, &n); err != nil {
			return fmt.Errorf("decode node: %w", err)
		}
		row = nodeRow(&n, raw)
	case stageDeps:
		var d exportDep
		if err := json.Unmarshal(raw, &d); err != nil {
			return fmt.Errorf("decode dependency: %w", err)
		}
		row = depRow(&d, raw)
	default:
		var h exportHistoryEntry
		if err := json.Unmarshal(raw, &h); err != nil {
			return fmt.Errorf("decode history entry: %w", err)
		}
		row = historyRow(&h, raw)
	}
	if err := insertRow(ctx, tx, kind, st.counts[kind], row); err != nil {
		return err
	}
	st.counts[kind]++
	st.presence[kind] = arrayFilled
	return nil
}

// insertRow stores one element at (kind, ord) with its id tuples.
func insertRow(ctx context.Context, tx *sql.Tx, kind stageKind, ord int, row stagedRow) error {
	// Store the element's text under its place and sort keys.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO stage_rows (kind, ord, k1, k2, k3, body) VALUES (?, ?, ?, ?, ?, ?)`,
		int(kind), ord, row.k1, row.k2, row.k3, row.body); err != nil {
		return fmt.Errorf("store row: %w", err)
	}
	for _, t := range row.ids {
		// Record one non-UTF-8 id of the element for ids_checksum.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO stage_ids (src, kind, key, hex) VALUES (?, ?, ?, ?)`,
			int(kind), t.Kind, t.Key, t.Hex); err != nil {
			return fmt.Errorf("store id: %w", err)
		}
	}
	return nil
}

func nodeRow(n *exportNode, body []byte) stagedRow {
	ids := appendIDTuples(nil, "uid", n.ID, n.UID)
	return stagedRow{k1: n.ID, body: body, ids: appendHistoryTuples(ids, n.History)}
}

func depRow(d *exportDep, body []byte) stagedRow {
	return stagedRow{k1: d.FromID, k2: d.ToID, body: body}
}

// historyRow keys an entry as sortHistory sorts: task uid, time, entry id.
func historyRow(h *exportHistoryEntry, body []byte) stagedRow {
	return stagedRow{k1: h.TaskUID, k2: h.TS, k3: h.ID, body: body,
		ids: appendHistoryTuples(nil, []exportHistoryEntry{*h})}
}

// decodeRepeated decodes the next occurrence of an already-seen array key with
// encoding/json itself, into the slice the earlier occurrences left (built
// from the staged rows the first time, by appending one element at a time as
// the decoder does, so even the slice capacity matches). The staged rows are
// rewritten from the final slices by restageRepeated. Memory is the size of
// the repeated array, as for the historical reader.
func (st *StagedExport) decodeRepeated(ctx context.Context, tx *sql.Tx, dec *json.Decoder, kind stageKind) error {
	var err error
	switch kind {
	case stageNodes:
		err = decodeRepeatedInto[exportNode](ctx, st, tx, dec, kind)
	case stageDeps:
		err = decodeRepeatedInto[exportDep](ctx, st, tx, dec, kind)
	default:
		err = decodeRepeatedInto[exportHistoryEntry](ctx, st, tx, dec, kind)
	}
	if err != nil {
		return fmt.Errorf("decode repeated %s array: %w", kind, err)
	}
	return nil
}

func decodeRepeatedInto[T any](ctx context.Context, st *StagedExport, tx *sql.Tx, dec *json.Decoder, kind stageKind) error {
	slot, ok := st.replay[kind].(*[]T)
	if !ok {
		list, err := loadStaged[T](ctx, st, tx, kind)
		if err != nil {
			return err
		}
		slot = &list
		st.replay[kind] = slot
	}
	if err := dec.Decode(slot); err != nil {
		return err
	}
	return nil
}

// loadStaged rebuilds the slice the decoder holds after the staged array: nil
// when the file had null, else the elements appended in file order.
func loadStaged[T any](ctx context.Context, st *StagedExport, tx *sql.Tx, kind stageKind) ([]T, error) {
	var list []T
	if st.presence[kind] == arrayAbsent {
		return list, nil
	}
	list = []T{}
	// Read the elements staged so far, in file order.
	rows, err := tx.QueryContext(ctx, stagedRowsInFileOrderSQL, int(kind))
	if err != nil {
		return nil, fmt.Errorf("read staged %s rows: %w", kind, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("scan staged %s row: %w", kind, err)
		}
		var v T
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, fmt.Errorf("decode staged %s row: %w", kind, err)
		}
		list = append(list, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read staged %s rows: %w", kind, err)
	}
	return list, nil
}

// restageRepeated replaces the staged rows of every repeated array with the
// elements encoding/json decoded.
func (st *StagedExport) restageRepeated(ctx context.Context, tx *sql.Tx) error {
	for kind := stageKind(0); kind < stageKindCount; kind++ {
		var err error
		switch list := st.replay[kind].(type) {
		case *[]exportNode:
			err = restageList(ctx, st, tx, kind, *list, nodeRow)
		case *[]exportDep:
			err = restageList(ctx, st, tx, kind, *list, depRow)
		case *[]exportHistoryEntry:
			err = restageList(ctx, st, tx, kind, *list, historyRow)
		}
		if err != nil {
			return fmt.Errorf("restage repeated %s array: %w", kind, err)
		}
	}
	return nil
}

func restageList[T any](ctx context.Context, st *StagedExport, tx *sql.Tx, kind stageKind, list []T,
	rowOf func(*T, []byte) stagedRow) error {
	// Drop what the earlier occurrences staged; the decoded slice replaces it.
	if _, err := tx.ExecContext(ctx, `DELETE FROM stage_rows WHERE kind = ?`, int(kind)); err != nil {
		return fmt.Errorf("clear staged rows: %w", err)
	}
	// Drop the non-UTF-8 id tuples of those rows.
	if _, err := tx.ExecContext(ctx, `DELETE FROM stage_ids WHERE src = ?`, int(kind)); err != nil {
		return fmt.Errorf("clear staged ids: %w", err)
	}
	st.counts[kind], st.presence[kind] = 0, arrayAbsent
	if list == nil {
		return nil
	}
	st.presence[kind] = arrayEmpty
	for i := range list {
		body, err := json.Marshal(list[i])
		if err != nil {
			return fmt.Errorf("encode element %d: %w", i, err)
		}
		if err := insertRow(ctx, tx, kind, i, rowOf(&list[i], body)); err != nil {
			return fmt.Errorf("element %d: %w", i, err)
		}
		st.counts[kind]++
		st.presence[kind] = arrayFilled
	}
	return nil
}

// appendIDTuples adds the tuple of value when it is not valid UTF-8, built as
// computeIDsChecksum builds it.
func appendIDTuples(ids []idTuple, kind, key, value string) []idTuple {
	if utf8.ValidString(value) {
		return ids
	}
	return append(ids, idTuple{kind, jsonSpelling(key), hex.EncodeToString([]byte(value))})
}

// appendHistoryTuples adds the task_uid and event_id tuples of each entry.
func appendHistoryTuples(ids []idTuple, list []exportHistoryEntry) []idTuple {
	for i := range list {
		ids = appendIDTuples(ids, "task_uid", list[i].ID, list[i].TaskUID)
		ids = appendIDTuples(ids, "event_id", list[i].ID, list[i].EventID)
	}
	return ids
}
