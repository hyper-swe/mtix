// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/hyper-swe/mtix/internal/model"
)

// emittingAuthorsKey is the meta key holding the author ids this replica has
// emitted under (a JSON array, at most model.MaxVectorClockEntries long).
// They are the authors the vector-clock prune never drops (MTIX-95.16): a
// dropped emitting author would restart its counter at 1, and its next event
// would compare as older than, or concurrent with, that author's own earlier
// events on a peer that still holds the old counter.
const emittingAuthorsKey = "meta.sync.vc_emitting_authors"

// loadEmittingAuthors returns the recorded emitting authors and the raw
// stored value (for storeEmittingAuthors to skip an unchanged write). A
// replica with no list yet (upgraded from a build without it) is seeded with
// its default author: the MTIX_AUTHOR_ID, stored author_id or fallback that
// resolveEmitAuthor yields. A malformed list is reported through the
// injected logger and treated as empty plus that seed; a write is never
// blocked by it.
func loadEmittingAuthors(ctx context.Context, tx *sql.Tx) (authors []string, stored string, err error) {
	seed := sanitizeAuthorID(resolveEmitAuthor(ctx, tx, ""))
	err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, emittingAuthorsKey).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return []string{seed}, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read emitting authors: %w", err)
	}
	if jsonErr := json.Unmarshal([]byte(stored), &authors); jsonErr != nil {
		vcLogger(ctx).Warn("vector clock: emitting-authors list is malformed; rebuilding it",
			"key", emittingAuthorsKey, "error", jsonErr)
		return []string{seed}, stored, nil
	}
	return authors, stored, nil
}

// storeEmittingAuthors persists the emitting authors that are still in vc,
// sorted and without duplicates, so the list stays within the clock's cap.
// It writes only when the encoded list differs from prev, the value loaded.
func storeEmittingAuthors(ctx context.Context, tx *sql.Tx, authors []string, vc model.VectorClock, prev string) error {
	seen := make(map[string]bool, len(authors))
	kept := make([]string, 0, len(authors))
	for _, a := range authors {
		if _, ok := vc[a]; ok && !seen[a] {
			seen[a] = true
			kept = append(kept, a)
		}
	}
	sort.Strings(kept)
	encoded, err := json.Marshal(kept)
	if err != nil {
		return fmt.Errorf("encode emitting authors: %w", err)
	}
	if string(encoded) == prev {
		return nil
	}
	// Record which authors this replica emits under, for the clock prune.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		emittingAuthorsKey, string(encoded)); err != nil {
		return fmt.Errorf("write emitting authors: %w", err)
	}
	return nil
}
