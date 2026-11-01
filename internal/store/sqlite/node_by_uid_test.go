// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestLiveNodeIDByUID_FoundMissingAndDeleted(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	node := makeRootNode("PROJ-1", "PROJ", "Has a uid", time.Now().UTC())
	require.NoError(t, s.CreateNode(ctx, node))
	var uid string
	require.NoError(t, s.QueryRow(ctx, `SELECT uid FROM nodes WHERE id = ?`, "PROJ-1").Scan(&uid))
	require.NotEmpty(t, uid)

	got, err := s.LiveNodeIDByUID(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1", got)

	_, err = s.LiveNodeIDByUID(ctx, "no-such-uid")
	assert.ErrorIs(t, err, model.ErrNotFound)

	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))
	_, err = s.LiveNodeIDByUID(ctx, uid)
	assert.ErrorIs(t, err, model.ErrNotFound, "a soft-deleted task is not live")
}
