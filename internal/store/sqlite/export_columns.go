// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
)

// errUnreadableNodeColumn marks a nodes JSON column whose text does not
// parse (MTIX-95.31.1). Export fails on it rather than write a board of
// record without that column; recover salvages the node without it.
var errUnreadableNodeColumn = errors.New("unreadable node column")

// exportNodeJSON holds the nodes columns scanExportNode decodes rather than
// copies: the nullable integers and the JSON-text columns (MTIX-95.31.1).
type exportNodeJSON struct {
	estimateMin, actualMin sql.NullInt64
	codeRefs, commitRefs   sql.NullString
	annotations, activity  sql.NullString
}

// decodeInto sets n's nullable integers and decodes its JSON columns into
// the structure mtix show --json returns (MTIX-95.31.1). Every column is
// attempted: one that does not parse is left empty on n and reported,
// naming the node and the column and wrapping errUnreadableNodeColumn.
func (j *exportNodeJSON) decodeInto(n *exportNode) error {
	n.EstimateMin = nullIntPtr(j.estimateMin)
	n.ActualMin = nullIntPtr(j.actualMin)
	return errors.Join(
		decodeNodeColumn(n.ID, "code_refs", j.codeRefs, &n.CodeRefs),
		decodeNodeColumn(n.ID, "commit_refs", j.commitRefs, &n.CommitRefs),
		decodeNodeColumn(n.ID, "annotations", j.annotations, &n.Annotations),
		decodeNodeColumn(n.ID, "activity", j.activity, &n.Activity),
	)
}

// decodeNodeColumn decodes one JSON-array column of node id into dest
// (MTIX-95.31.1). NULL, empty text and "null" decode to an empty list. Text
// that does not parse leaves dest empty, never half-filled.
func decodeNodeColumn[T any](id, column string, text sql.NullString, dest *[]T) error {
	if err := unmarshalJSONField(text, dest); err != nil {
		*dest = nil
		return fmt.Errorf("node %s column %s: %w: %w", id, column, errUnreadableNodeColumn, err)
	}
	return nil
}

// nullIntPtr converts a nullable integer column to the *int the export
// carries (nil for NULL).
func nullIntPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	i := int(v.Int64)
	return &i
}

// exportNodeColumnValues are the SQL values import writes for the columns
// exportNodeJSON decodes (MTIX-95.31.1).
type exportNodeColumnValues struct {
	estimateMin, actualMin sql.NullInt64
	codeRefs, commitRefs   sql.NullString
	annotations, activity  sql.NullString
}

// encodeNodeColumns encodes n's structured columns for import the way
// CreateNode, SetAnnotations and the activity writers store them: compact
// JSON, and NULL for an empty list, which every reader treats as empty
// (MTIX-95.31.1).
func encodeNodeColumns(n *exportNode) (exportNodeColumnValues, error) {
	v := exportNodeColumnValues{
		estimateMin: nullableInt(n.EstimateMin),
		actualMin:   nullableInt(n.ActualMin),
	}
	var errs [4]error
	v.codeRefs, errs[0] = marshalJSONField(n.CodeRefs)
	v.commitRefs, errs[1] = marshalJSONField(n.CommitRefs)
	v.annotations, errs[2] = marshalJSONField(n.Annotations)
	v.activity, errs[3] = marshalJSONField(n.Activity)
	if err := errors.Join(errs[:]...); err != nil {
		return v, fmt.Errorf("encode columns of node %s: %w", n.ID, err)
	}
	return v, nil
}
