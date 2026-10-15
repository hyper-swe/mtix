// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestRenumberSubtree_NonASCIIID_RewritesEveryReference: an id with a
// non-ASCII character (one that arrives by sync) renumbers like any other.
// Parent AA-1é holds .1 .2 .4 and .4's children .1 .2 .21; renumbering .4
// to 5 must move its children to AA-1é.5.1, .5.2 and .5.21, and must move
// each child's parent_id and both dependency endpoints with them. A byte
// offset passed to SQLite SUBSTR, which counts characters, wrote AA-1é.51,
// AA-1é.52 and AA-1é.521 instead.
func TestRenumberSubtree_NonASCIIID_RewritesEveryReference(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, n := range []seqEntry{
		{"AA-1é", 1}, {"AA-1é.1", 1}, {"AA-1é.2", 2}, {"AA-1é.4", 4},
		{"AA-1é.4.1", 1}, {"AA-1é.4.2", 2}, {"AA-1é.4.21", 21},
	} {
		require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)), n.id)
	}
	now := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	for _, d := range []model.Dependency{
		{FromID: "AA-1é.4.2", ToID: "AA-1é.1", DepType: model.DepTypeBlocks, CreatedAt: now, CreatedBy: "test"},
		{FromID: "AA-1é.2", ToID: "AA-1é.4.21", DepType: model.DepTypeBlocks, CreatedAt: now, CreatedBy: "test"},
	} {
		require.NoError(t, s.AddDependency(ctx, &d))
	}

	require.NoError(t, s.RenumberSubtree(ctx, "AA-1é.4", 5))

	for _, id := range []string{"AA-1é.5", "AA-1é.5.1", "AA-1é.5.2", "AA-1é.5.21"} {
		n, err := s.GetNode(ctx, id)
		require.NoError(t, err, id)
		require.Equal(t, model.ParseIDParent(id), n.ParentID, id)
	}
	for _, id := range []string{"AA-1é.4", "AA-1é.51", "AA-1é.52", "AA-1é.521"} {
		_, err := s.GetNode(ctx, id)
		require.ErrorIs(t, err, model.ErrNotFound, id)
	}
	require.Equal(t, 5, counterOf(t, s, "AA:AA-1é"))

	var from, to string
	row := s.ReadDB().QueryRowContext(ctx, `SELECT from_id, to_id FROM dependencies WHERE to_id = ?`, "AA-1é.1")
	require.NoError(t, row.Scan(&from, &to))
	require.Equal(t, "AA-1é.5.2", from)
	row = s.ReadDB().QueryRowContext(ctx, `SELECT from_id, to_id FROM dependencies WHERE from_id = ?`, "AA-1é.2")
	require.NoError(t, row.Scan(&from, &to))
	require.Equal(t, "AA-1é.5.21", to)
}
