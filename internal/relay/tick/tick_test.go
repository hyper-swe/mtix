// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package tick_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/stretchr/testify/require"
)

// recorder notes the order the halves ran in.
type recorder struct {
	order     *[]string
	published int
	stats     ingest.Stats
	pubErr    error
	ingErr    error
}

func (r *recorder) PublishPending(context.Context) (int, error) {
	*r.order = append(*r.order, "publish")
	return r.published, r.pubErr
}

func (r *recorder) IngestAll(context.Context) (ingest.Stats, error) {
	*r.order = append(*r.order, "ingest")
	return r.stats, r.ingErr
}

// TestRun_PublishesBeforeIngesting pins the FR-21 §6.6 order. Publishing
// first means a tick-only peer running exactly one pass per session
// still gets its own work onto the medium, and ingest then leaves fresh
// journal rows for the dispatch phase that follows to fire hooks from.
func TestRun_PublishesBeforeIngesting(t *testing.T) {
	var order []string
	r := &recorder{order: &order, published: 3, stats: ingest.Stats{Applied: 2}}

	res, err := tick.Run(context.Background(), r, r)
	require.NoError(t, err)
	require.Equal(t, []string{"publish", "ingest"}, order)
	require.Equal(t, 3, res.Published)
	require.Equal(t, 2, res.Ingest.Applied)
}

// TestRun_BothHalvesRunEvenWhenOneFails is the independence rule: a
// medium that cannot be written says nothing about whether it can be
// read, and a peer that cannot publish should still converge on what
// others sent it.
func TestRun_BothHalvesRunEvenWhenOneFails(t *testing.T) {
	pubBoom := errors.New("relay directory is unwritable")
	ingBoom := errors.New("relay directory is unreadable")

	tests := []struct {
		name   string
		pubErr error
		ingErr error
		want   []error
	}{
		{"publish fails", pubBoom, nil, []error{pubBoom}},
		{"ingest fails", nil, ingBoom, []error{ingBoom}},
		{"both fail", pubBoom, ingBoom, []error{pubBoom, ingBoom}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var order []string
			r := &recorder{order: &order, pubErr: tt.pubErr, ingErr: tt.ingErr}

			_, err := tick.Run(context.Background(), r, r)
			require.Error(t, err)
			require.Equal(t, []string{"publish", "ingest"}, order,
				"both halves run regardless of which failed")
			for _, want := range tt.want {
				require.ErrorIs(t, err, want,
					"a caller must learn about every failure in the pass, not the first one")
			}
		})
	}
}

// TestRun_NilHalvesAreSkipped covers a peer configured to read but not
// publish, or the reverse.
func TestRun_NilHalvesAreSkipped(t *testing.T) {
	var order []string
	r := &recorder{order: &order, published: 1, stats: ingest.Stats{Applied: 1}}

	t.Run("ingest only", func(t *testing.T) {
		order = nil
		res, err := tick.Run(context.Background(), nil, r)
		require.NoError(t, err)
		require.Equal(t, []string{"ingest"}, order)
		require.Zero(t, res.Published)
	})
	t.Run("publish only", func(t *testing.T) {
		order = nil
		res, err := tick.Run(context.Background(), r, nil)
		require.NoError(t, err)
		require.Equal(t, []string{"publish"}, order)
		require.Zero(t, res.Ingest.Applied)
	})
	t.Run("neither", func(t *testing.T) {
		order = nil
		res, err := tick.Run(context.Background(), nil, nil)
		require.NoError(t, err)
		require.Empty(t, order)
		require.Zero(t, res.Published)
	})
}
