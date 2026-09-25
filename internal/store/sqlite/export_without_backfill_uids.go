// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"slices"

	"github.com/hyper-swe/mtix/internal/model"
)

// WithoutBackfillUIDs returns data as this version exported the same store
// before the open-time backfill (MTIX-95.31.9) gave its tasks without a uid
// one (MTIX-95.31.11, FR-15.2h): every backfill uid (model.IsBackfillUID)
// is left out, every other uid is kept, and the checksum is computed again
// over the nodes and dependencies in their order, as Export computes it
// (FR-7.8). A conflict baseline written before the backfill minted those
// uids can then be recognized. data is not changed; the result shares its
// dependencies, agents and sessions. Returns ErrInvalidInput for a nil
// export.
func WithoutBackfillUIDs(data *ExportData) (*ExportData, error) {
	if data == nil {
		return nil, fmt.Errorf("nil export data: %w", model.ErrInvalidInput)
	}
	form := *data
	form.Nodes = slices.Clone(data.Nodes) // a copy, nil kept nil: data is not changed
	for i := range form.Nodes {
		if model.IsBackfillUID(form.Nodes[i].UID) {
			form.Nodes[i].UID = ""
		}
	}
	checksum, err := computeExportChecksum(form.Nodes, form.Dependencies)
	if err != nil {
		return nil, fmt.Errorf("compute checksum without backfill uids: %w", err)
	}
	form.Checksum = checksum
	return &form, nil
}
