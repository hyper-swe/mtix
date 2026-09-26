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
// record without that column; recover takes the column from the mirror's
// copy of the node, or salvages the node without it (MTIX-95.31.3).
var errUnreadableNodeColumn = errors.New("unreadable node column")

// The nodes JSON columns scanExportNode decodes, by column name.
const (
	columnCodeRefs    = "code_refs"
	columnCommitRefs  = "commit_refs"
	columnAnnotations = "annotations"
	columnActivity    = "activity"
)

// unreadableColumnError reports one JSON column of one node whose text does
// not parse (MTIX-95.31.1). It names the node and the column, so recover
// can restore that column from the mirror (MTIX-95.31.3), and it wraps
// errUnreadableNodeColumn and the parse error.
type unreadableColumnError struct {
	nodeID, column string
	cause          error
}

// Error names the node and the column, then the parse error.
func (e *unreadableColumnError) Error() string {
	return fmt.Sprintf("node %s column %s: %v: %v", e.nodeID, e.column, errUnreadableNodeColumn, e.cause)
}

// Unwrap exposes errUnreadableNodeColumn and the parse error to errors.Is
// and errors.As.
func (e *unreadableColumnError) Unwrap() []error {
	return []error{errUnreadableNodeColumn, e.cause}
}

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
		decodeNodeColumn(n.ID, columnCodeRefs, j.codeRefs, &n.CodeRefs),
		decodeNodeColumn(n.ID, columnCommitRefs, j.commitRefs, &n.CommitRefs),
		decodeNodeColumn(n.ID, columnAnnotations, j.annotations, &n.Annotations),
		decodeNodeColumn(n.ID, columnActivity, j.activity, &n.Activity),
	)
}

// decodeNodeColumn decodes one JSON-array column of node id into dest
// (MTIX-95.31.1). NULL, empty text and "null" decode to an empty list. Text
// that does not parse leaves dest empty, never half-filled, and is reported
// as an unreadableColumnError (MTIX-95.31.3).
func decodeNodeColumn[T any](id, column string, text sql.NullString, dest *[]T) error {
	if err := unmarshalJSONField(text, dest); err != nil {
		*dest = nil
		return &unreadableColumnError{nodeID: id, column: column, cause: err}
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
