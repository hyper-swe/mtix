// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Real paired relay measurements and the independent FR-21 fresh-success
// reference. These helpers do not select or relax a performance threshold.
package benchmarks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/segment"
)

type relayPublishMeasurement struct {
	Count   int
	Elapsed time.Duration
	Err     error
}

type relayPublishPair struct {
	Reference      relayPublishMeasurement
	Candidate      relayPublishMeasurement
	ReferenceFirst bool
}

// TestRelayPublishReference_CompleteFreshBatches checks existing publication
// invariants, not a new timing threshold or a claimed regression reproduction.
func TestRelayPublishReference_CompleteFreshBatches(t *testing.T) {
	for _, referenceFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("referenceFirst=%t", referenceFirst), func(t *testing.T) {
			pair := measureRelayPublishPair(t, referenceFirst)
			require.Equal(t, referenceFirst, pair.ReferenceFirst)
			require.Equal(t, 200, pair.Reference.Count)
			require.Equal(t, 200, pair.Candidate.Count)
		})
	}
}

func TestRelayPublishPair_OrderIsExplicit(t *testing.T) {
	for _, referenceFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("referenceFirst=%t", referenceFirst), func(t *testing.T) {
			var order []string
			measure := func(name string) func() relayPublishMeasurement {
				return func() relayPublishMeasurement {
					order = append(order, name)
					return relayPublishMeasurement{Count: 200, Elapsed: time.Nanosecond}
				}
			}
			pair := runRelayPublishPair(measure("reference"), measure("candidate"), referenceFirst)
			want := []string{"candidate", "reference"}
			if referenceFirst {
				want = []string{"reference", "candidate"}
			}
			require.Equal(t, want, order)
			require.Equal(t, referenceFirst, pair.ReferenceFirst)
		})
	}
}

func newFullRelayRig(tb testing.TB) *relayRig {
	tb.Helper()
	r := newRelayRig(tb)
	r.journal(tb, 200)
	return r
}

// Both fixtures and the candidate constructor precede both timers. Validation
// follows both timers, so its disk reads cannot warm the other timed operation.
func measureRelayPublishPair(tb testing.TB, referenceFirst bool) relayPublishPair {
	tb.Helper()
	var reference, candidate *relayRig
	if referenceFirst {
		reference, candidate = newFullRelayRig(tb), newFullRelayRig(tb)
	} else {
		candidate, reference = newFullRelayRig(tb), newFullRelayRig(tb)
	}
	p := candidate.publisher(tb)
	ref := func() relayPublishMeasurement {
		return measureRelayOperation(func() (int, error) {
			return publishRelayReference(context.Background(), reference)
		})
	}
	current := func() relayPublishMeasurement {
		return measureRelayOperation(func() (int, error) {
			return p.PublishPending(context.Background())
		})
	}
	pair := runRelayPublishPair(ref, current, referenceFirst)
	for _, result := range []relayPublishMeasurement{pair.Reference, pair.Candidate} {
		require.NoError(tb, result.Err)
		require.Equal(tb, 200, result.Count)
		require.Positive(tb, result.Elapsed)
	}
	validateRelayPublished(tb, reference)
	validateRelayPublished(tb, candidate)
	require.Equal(tb, int64(200), p.Stats().Published)
	return pair
}

func measureRelayOperation(publish func() (int, error)) relayPublishMeasurement {
	start := time.Now()
	n, err := publish()
	return relayPublishMeasurement{Count: n, Elapsed: time.Since(start), Err: err}
}

func runRelayPublishPair(reference, candidate func() relayPublishMeasurement, referenceFirst bool) relayPublishPair {
	result := relayPublishPair{ReferenceFirst: referenceFirst}
	if referenceFirst {
		result.Reference, result.Candidate = reference(), candidate()
	} else {
		result.Candidate, result.Reference = candidate(), reference()
	}
	return result
}

// publishRelayReference freezes the fresh-success work of publisher.go at
// 1cce234ba5182490702d683c07b4c186441101e1. It never calls Publisher or its
// private publish path. Store/JSON/segment primitives remain shared, so a
// common-path slowdown can cancel in the ratio; the quiet reference ceiling
// separately guards the total cost. Do not recapture this from a mutant.
func publishRelayReference(ctx context.Context, r *relayRig) (appended int, err error) {
	pos, err := r.store.RelayPushCursor(ctx)
	if err != nil {
		return 0, fmt.Errorf("reference cursor: %w", err)
	}
	if pos.Seq != 0 || pos.PubEpoch != 1 || pos.NextRS != 1 {
		return 0, fmt.Errorf("reference requires a fresh cursor, got %+v", pos)
	}
	if err := verifyFreshRelayReference(ctx, r); err != nil {
		return 0, err
	}
	rows, err := r.store.ReadRelayJournalSince(ctx, pos.Seq, 200)
	if err != nil {
		return 0, fmt.Errorf("reference journal: %w", err)
	}
	if len(rows) != 200 {
		return 0, fmt.Errorf("reference journal: got %d events, want 200", len(rows))
	}
	w, err := segment.NewWriter(segment.WriterConfig{
		Dir: r.segDir, PeerID: relayPeerID, Key: relayKey, KeyEpoch: 1,
		PubEpoch: pos.PubEpoch, NextRS: pos.NextRS,
	})
	if err != nil {
		return 0, fmt.Errorf("reference writer: %w", err)
	}
	defer func() { err = errors.Join(err, w.Close()) }()
	for _, row := range rows {
		payload, marshalErr := json.Marshal(row.Event)
		if marshalErr != nil {
			return appended, fmt.Errorf("reference encode: %w", marshalErr)
		}
		if _, appendErr := w.Append(payload); appendErr != nil {
			return appended, fmt.Errorf("reference append: %w", appendErr)
		}
		appended++
	}
	if err := r.store.AdvanceRelayPushCursor(ctx, rows[len(rows)-1].Seq, w.NextRS()); err != nil {
		return appended, fmt.Errorf("reference advance: %w", err)
	}
	return appended, nil
}

func verifyFreshRelayReference(ctx context.Context, r *relayRig) error {
	if _, err := r.store.JournalGeneration(ctx); err != nil {
		return fmt.Errorf("reference generation: %w", err)
	}
	if _, err := r.store.JournalTail(ctx); err != nil {
		return fmt.Errorf("reference tail: %w", err)
	}
	segments, foreign, err := segment.ListSegments(r.segDir)
	if err != nil {
		return fmt.Errorf("reference segments: %w", err)
	}
	if len(segments) != 0 || len(foreign) != 0 {
		return fmt.Errorf("reference requires an empty segment directory")
	}
	return nil
}

func validateRelayPublished(tb testing.TB, r *relayRig) {
	tb.Helper()
	ctx := context.Background()
	rows, err := r.store.ReadRelayJournalSince(ctx, 0, 201)
	require.NoError(tb, err)
	require.Len(tb, rows, 200)
	pos, err := r.store.RelayPushCursor(ctx)
	require.NoError(tb, err)
	require.Equal(tb, rows[len(rows)-1].Seq, pos.Seq)
	require.Equal(tb, uint64(201), pos.NextRS)
	segments, foreign, err := segment.ListSegments(r.segDir)
	require.NoError(tb, err)
	require.Empty(tb, foreign)
	require.NotEmpty(tb, segments)
	appended := 0
	for _, s := range segments {
		result, scanErr := segment.ScanFile(s.Path, segment.ScanOptions{
			Key: relayKey, ExpectPeerID: relayPeerID,
		})
		require.NoError(tb, scanErr)
		for _, record := range result.Records {
			require.Less(tb, appended, len(rows))
			want, marshalErr := json.Marshal(rows[appended].Event)
			require.NoError(tb, marshalErr)
			require.Equal(tb, want, record.Payload)
			appended++
		}
	}
	require.Equal(tb, 200, appended)
	pending, err := r.store.ReadRelayJournalSince(ctx, pos.Seq, 1)
	require.NoError(tb, err)
	require.Empty(tb, pending)
}
