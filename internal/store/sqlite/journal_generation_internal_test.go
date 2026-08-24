// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestJournalGeneration_CorruptValueIsRefusedNotGuessed: the witness is only
// worth having if an unreadable one is loud. A non-integer value must return
// an error the caller treats as "changed" (FR-21 §5.7 v1.3.6, fail-secure),
// never a plausible-looking 0 — which reads as "never discarded" and would
// silently validate an attestation taken against a different journal. Written
// against the raw DB because no exported setter can produce this state, which
// is the point: only corruption can.
func TestJournalGeneration_CorruptValueIsRefusedNotGuessed(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	ctx := context.Background()

	_, err := raw.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		journalGenerationKey, "not-a-number")
	require.NoError(t, err)

	gen, err := s.JournalGeneration(ctx)
	require.Error(t, err, "a corrupt witness must be refused, not silently read as generation 0")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.Zero(t, gen)
}

// TestJournalGeneration_QueryFailureIsReportedNotZero: the other half of
// fail-secure. Schema damage must surface as an error, not as generation 0 —
// the same trap as the corrupt value, reached by a different route, and the
// one a caller is most likely to mistake for "this store was never discarded".
func TestJournalGeneration_QueryFailureIsReportedNotZero(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	ctx := context.Background()

	_, err := raw.ExecContext(ctx, `DROP TABLE meta`)
	require.NoError(t, err)

	gen, err := s.JournalGeneration(ctx)
	require.Error(t, err, "an unreadable witness must never be reported as generation 0")
	require.ErrorContains(t, err, "read journal generation")
	require.Zero(t, gen)
}
