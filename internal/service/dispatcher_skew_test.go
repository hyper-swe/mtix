// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type fakeHeartbeatStore struct {
	rows     []sqlite.DispatcherHeartbeat
	floor    int
	listErr  error
	floorErr error
	asked    [2]string
}

func (f *fakeHeartbeatStore) ListDispatcherHeartbeats(_ context.Context, hostID, uid string) ([]sqlite.DispatcherHeartbeat, error) {
	f.asked = [2]string{hostID, uid}
	return f.rows, f.listErr
}

func (f *fakeHeartbeatStore) MinDispatchProtocol(context.Context) (int, error) {
	return f.floor, f.floorErr
}

func TestDispatcherSkewStatuses_ClassifiesOlderAndBelowFloor(t *testing.T) {
	store := &fakeHeartbeatStore{floor: 2, rows: []sqlite.DispatcherHeartbeat{
		{PID: 1, Protocol: 1, Version: "0.6.0"},
		{PID: 2, Protocol: 2, Version: "0.6.1"},
		{PID: 3, Protocol: 3, Version: "0.7.0"},
	}}
	got, err := NewDispatcherSkew(store, "host-a", "501", 2).Statuses(context.Background())
	require.NoError(t, err)
	assert.Equal(t, [2]string{"host-a", "501"}, store.asked, "only this host and user")
	assert.Equal(t, []DispatcherStatus{
		{PID: 1, Protocol: 1, Version: "0.6.0", Older: true, BelowFloor: true, Floor: 2},
		{PID: 2, Protocol: 2, Version: "0.6.1", Floor: 2},
		{PID: 3, Protocol: 3, Version: "0.7.0", Floor: 2},
	}, got)
}

func TestDispatcherSkewStatuses_StoreErrors_Wrapped(t *testing.T) {
	boom := errors.New("database locked")
	_, err := NewDispatcherSkew(&fakeHeartbeatStore{listErr: boom}, "h", "1", 1).Statuses(context.Background())
	assert.ErrorIs(t, err, boom)
	_, err = NewDispatcherSkew(&fakeHeartbeatStore{floorErr: boom}, "h", "1", 1).Statuses(context.Background())
	assert.ErrorIs(t, err, boom)
}

func TestDispatcherStatusNeedsAttention_OlderOrBelowFloor(t *testing.T) {
	assert.True(t, DispatcherStatus{Older: true}.NeedsAttention())
	assert.True(t, DispatcherStatus{BelowFloor: true}.NeedsAttention())
	assert.False(t, DispatcherStatus{}.NeedsAttention())
}
