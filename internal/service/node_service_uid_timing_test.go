// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/binary"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// slowSequenceStore is a store whose sequence step waits, as NextSequence
// waits for the write lock while another command writes.
type slowSequenceStore struct {
	store.Store
	wait time.Duration
}

// NextSequence waits, then hands out the next sequence.
func (s *slowSequenceStore) NextSequence(ctx context.Context, key string) (int, error) {
	time.Sleep(s.wait)
	return s.Store.NextSequence(ctx, key)
}

// TestCreateNode_SequenceWaitsForLock_UIDMintedAtTheClockRead verifies a
// node's uid is minted right after its creation time is read, before the
// sequence step that can wait for the write lock (MTIX-95.31.4), so the
// uid's time and created_at stay together.
func TestCreateNode_SequenceWaitsForLock_UIDMintedAtTheClockRead(t *testing.T) {
	st, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	svc := service.NewNodeService(&slowSequenceStore{Store: st, wait: 1200 * time.Millisecond}, nil, nil, nil,
		func() time.Time { return time.Now().UTC() })

	node, err := svc.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "PROJ", Title: "Probe", Creator: "a"})
	require.NoError(t, err)
	u, err := uuid.Parse(node.UID)
	require.NoError(t, err)
	minted := time.UnixMilli(int64(binary.BigEndian.Uint64(u[:8]) >> 16)) //nolint:gosec // 48-bit ms field
	assert.Less(t, minted.Sub(node.CreatedAt), 500*time.Millisecond, "the uid is minted before the sequence wait")
}
