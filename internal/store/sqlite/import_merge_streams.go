// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// carriesNodeColumns reports whether an export of the given schema_version
// carries the node columns schema 1.1.0 added (MTIX-95.31.1). An empty or
// unparsable version reads as 1.0.0, the safe reading: a merge then keeps
// the local values of those columns.
func carriesNodeColumns(version string) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil {
		return false
	}
	return major > 1 || (major == 1 && minor >= 1)
}

// keepLocalNodeColumns copies into n the local values of the columns schema
// 1.1.0 added (MTIX-95.31.1). A 1.0.0 file carries none of them, so their
// absence means "not carried", never "cleared"; before 1.1.0 a merge never
// wrote them either. Annotations and the activity stream are merged by
// mergeNodeStreams instead.
func keepLocalNodeColumns(n, local *exportNode) {
	n.PreviousStatus = local.PreviousStatus
	n.EstimateMin, n.ActualMin = local.EstimateMin, local.ActualMin
	n.CodeRefs, n.CommitRefs = local.CodeRefs, local.CommitRefs
	n.InvalidatedAt, n.InvalidatedBy = local.InvalidatedAt, local.InvalidatedBy
	n.InvalidationReason = local.InvalidationReason
	n.DeletedBy, n.Metadata, n.SessionID = local.DeletedBy, local.Metadata, local.SessionID
}

// mergeNodeStreams sets n's annotations and activity stream to their union
// with the local node's, and reports whether the union holds anything the
// local node did not (MTIX-95.31.1).
func mergeNodeStreams(n, local *exportNode) bool {
	var annotationsChanged, activityChanged bool
	n.Annotations, annotationsChanged = mergeAnnotations(local.Annotations, n.Annotations)
	n.Activity, activityChanged = mergeActivity(local.Activity, n.Activity)
	return annotationsChanged || activityChanged
}

// streamKey identifies an annotation or activity entry across stores.
type streamKey struct {
	id, kind, author, text, at string
}

// annotationKey keys an annotation by its id. An annotation without an id
// (none is written that way today) is keyed by its author, text and time,
// so merging the same file twice never duplicates it.
func annotationKey(a *model.Annotation) streamKey {
	if a.ID != "" {
		return streamKey{id: a.ID}
	}
	return streamKey{author: a.Author, text: a.Text, at: a.CreatedAt.UTC().Format(time.RFC3339Nano)}
}

// activityKey keys an activity entry by its id, type, author, text and
// time. Activity ids derive from a timestamp and are not unique across
// machines, so two different entries that share one are both kept.
func activityKey(e *model.ActivityEntry) streamKey {
	return streamKey{
		id: e.ID, kind: string(e.Type), author: e.Author, text: e.Text,
		at: e.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// mergeAnnotations returns the union of the local and incoming annotations
// by annotation id (MTIX-95.31.1). No local annotation is dropped. For an
// id both hold, the local copy wins unless the incoming copy is resolved
// and the local one is not: a resolution never regresses. When the union
// differs from the local list it is ordered by time, then id, as sync
// apply orders comments, so stores that merge each other's files converge.
func mergeAnnotations(local, incoming []model.Annotation) ([]model.Annotation, bool) {
	merged, changed := streamUnion(local, incoming, annotationKey,
		func(l, in *model.Annotation) bool { return in.Resolved && !l.Resolved })
	if changed {
		slices.SortStableFunc(merged, func(a, b model.Annotation) int {
			return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
		})
	}
	return merged, changed
}

// mergeActivity returns the union of the local and incoming activity
// entries (MTIX-95.31.1). Entries are immutable, so an entry both hold is
// kept once, as the local copy. When the union differs from the local list
// it is ordered by time, then id.
func mergeActivity(local, incoming []model.ActivityEntry) ([]model.ActivityEntry, bool) {
	merged, changed := streamUnion(local, incoming, activityKey, nil)
	if changed {
		slices.SortStableFunc(merged, func(a, b model.ActivityEntry) int {
			return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
		})
	}
	return merged, changed
}

// streamUnion returns local followed by every incoming entry whose key
// local lacks (MTIX-95.31.1). For a key both hold, the incoming copy
// replaces the local one only when takeIncoming says so (nil: never).
// changed reports whether the result differs from local.
func streamUnion[T any](
	local, incoming []T,
	keyOf func(*T) streamKey,
	takeIncoming func(local, incoming *T) bool,
) ([]T, bool) {
	merged := slices.Clone(local)
	index := make(map[streamKey]int, len(merged)+len(incoming))
	for i := range merged {
		index[keyOf(&merged[i])] = i
	}
	changed := false
	for i := range incoming {
		in := &incoming[i]
		key := keyOf(in)
		at, held := index[key]
		switch {
		case !held:
			index[key] = len(merged)
			merged = append(merged, *in)
			changed = true
		case takeIncoming != nil && takeIncoming(&merged[at], in):
			merged[at] = *in
			changed = true
		}
	}
	return merged, changed
}

// writeNodeStreams stores n's merged annotations and activity stream, the
// only columns a merge changes on a node whose content hash is unchanged
// (MTIX-95.31.1).
func writeNodeStreams(ctx context.Context, tx *sql.Tx, n *exportNode) error {
	cols, err := encodeNodeColumns(n)
	if err != nil {
		return err
	}
	// Parameterized update of the two merged JSON columns of one node.
	_, err = tx.ExecContext(ctx,
		`UPDATE nodes SET annotations = ?, activity = ? WHERE id = ?`,
		cols.annotations, cols.activity, n.ID)
	return err
}
