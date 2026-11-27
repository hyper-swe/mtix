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

// stageArray stages the elements of one array. A repeated key replaces the
// earlier array, as json.Unmarshal does.
func (st *StagedExport) stageArray(ctx context.Context, tx *sql.Tx, dec *json.Decoder, kind stageKind) error {
	if err := st.resetKind(ctx, tx, kind); err != nil {
		return err
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil // null: a nil slice
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("export array %d is not a JSON array: %w", kind, model.ErrInvalidInput)
	}
	st.presence[kind] = arrayEmpty
	for dec.More() {
		var raw json.RawMessage
		if decErr := dec.Decode(&raw); decErr != nil {
			return decErr
		}
		if rowErr := st.stageRow(ctx, tx, kind, raw); rowErr != nil {
			return rowErr
		}
	}
	_, err = dec.Token() // the closing bracket
	return err
}

// resetKind drops what an earlier occurrence of the same array staged.
func (st *StagedExport) resetKind(ctx context.Context, tx *sql.Tx, kind stageKind) error {
	st.counts[kind], st.presence[kind] = 0, arrayAbsent
	if _, err := tx.ExecContext(ctx, `DELETE FROM stage_rows WHERE kind = ?`, int(kind)); err != nil {
		return fmt.Errorf("reset staged rows: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM stage_ids WHERE src = ?`, int(kind)); err != nil {
		return fmt.Errorf("reset staged ids: %w", err)
	}
	return nil
}

// stageRow validates one element (it must decode as the historical reader
// decodes it), stores its raw JSON text under its keys, and records the ids
// that are not valid UTF-8.
func (st *StagedExport) stageRow(ctx context.Context, tx *sql.Tx, kind stageKind, raw json.RawMessage) error {
	k1, k2, ids, err := elementKeys(kind, raw)
	if err != nil {
		return err
	}
	ord := st.counts[kind]
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO stage_rows (kind, ord, k1, k2, body) VALUES (?, ?, ?, ?, ?)`,
		int(kind), ord, k1, k2, []byte(raw)); err != nil {
		return fmt.Errorf("stage row: %w", err)
	}
	for _, t := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO stage_ids (src, kind, key, hex) VALUES (?, ?, ?, ?)`,
			int(kind), t.Kind, t.Key, t.Hex); err != nil {
			return fmt.Errorf("stage id: %w", err)
		}
	}
	st.counts[kind]++
	st.presence[kind] = arrayFilled
	return nil
}

// elementKeys decodes one element and returns its index keys and its
// non-UTF-8 id tuples (the inputs of ids_checksum).
func elementKeys(kind stageKind, raw json.RawMessage) (k1, k2 string, ids []idTuple, err error) {
	switch kind {
	case stageNodes:
		var n exportNode
		if err = json.Unmarshal(raw, &n); err != nil {
			return "", "", nil, fmt.Errorf("decode staged node: %w", err)
		}
		ids = appendIDTuples(ids, "uid", n.ID, n.UID)
		ids = appendHistoryTuples(ids, n.History)
		return n.ID, "", ids, nil
	case stageDeps:
		var d exportDep
		if err = json.Unmarshal(raw, &d); err != nil {
			return "", "", nil, fmt.Errorf("decode staged dependency: %w", err)
		}
		return d.FromID, d.ToID, nil, nil
	default:
		var h exportHistoryEntry
		if err = json.Unmarshal(raw, &h); err != nil {
			return "", "", nil, fmt.Errorf("decode staged history entry: %w", err)
		}
		return h.ID, "", appendHistoryTuples(nil, []exportHistoryEntry{h}), nil
	}
}

// appendIDTuples adds the tuple of value when it is not valid UTF-8, built as
// computeIDsChecksum builds it.
func appendIDTuples(ids []idTuple, kind, key, value string) []idTuple {
	if utf8.ValidString(value) {
		return ids
	}
	return append(ids, idTuple{kind, jsonSpelling(key), hex.EncodeToString([]byte(value))})
}

func appendHistoryTuples(ids []idTuple, list []exportHistoryEntry) []idTuple {
	for i := range list {
		ids = appendIDTuples(ids, "task_uid", list[i].ID, list[i].TaskUID)
		ids = appendIDTuples(ids, "event_id", list[i].ID, list[i].EventID)
	}
	return ids
}
