// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type wakeLeaseServiceFixture struct {
	store  *sqlite.Store
	leases *service.WakeLeaseService
	path   string
}

func openWakeLeaseServiceFixture(t *testing.T, path string, now time.Time) wakeLeaseServiceFixture {
	t.Helper()
	backend, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	backend.SetClock(func() time.Time { return now })
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	return wakeLeaseServiceFixture{store: backend, leases: service.NewWakeLeaseService(backend), path: path}
}

func newWakeLeaseServiceFixture(t *testing.T) wakeLeaseServiceFixture {
	t.Helper()
	return openWakeLeaseServiceFixture(t, filepath.Join(t.TempDir(), "board.db"), wakeLeaseServiceTime())
}

func seedWakeLeaseForeignEvent(t *testing.T, f wakeLeaseServiceFixture) int64 {
	t.Helper()
	node := testutil.MakeNode(t)
	require.NoError(t, f.store.CreateNode(t.Context(), node))
	ann := model.Annotation{ID: "TEST-lease-event", Author: "TEST-sender", Text: "Handle this event", CreatedAt: wakeLeaseServiceTime(), Addressee: "TEST-other"}
	require.NoError(t, f.store.SetAnnotations(t.Context(), node.ID, []model.Annotation{ann}))
	events, err := f.store.InboxList(t.Context(), "TEST-other")
	require.NoError(t, err)
	require.Len(t, events, 1)
	return events[0].Seq
}

func TestWakeLeaseService_DirtyDeliveryBelowMark_RearmsSameOwner(t *testing.T) {
	f := newWakeLeaseServiceFixture(t)
	seq := seedWakeLeaseForeignEvent(t, f)
	key := wakeLeaseServiceKey()
	owner, err := f.leases.Acquire(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.LessOrEqual(t, seq, owner.HighWater)
	require.NoError(t, f.store.RecordInboxDelivery(t.Context(), key.AgentID, seq, "TEST-hook"))
	contender := openWakeLeaseServiceFixture(t, f.path, wakeLeaseServiceTime())
	held, err := contender.leases.Acquire(t.Context(), key, time.Minute)
	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	assert.Empty(t, held.Holder)
	assert.Zero(t, held.HighWater)
	assert.Equal(t, owner.ExpiresAt, held.ExpiresAt)
	result, err := f.leases.Release(t.Context(), key, owner.Holder, 2*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "relaunch", result.Outcome)
	assert.Equal(t, 1, result.Pending)
	require.NotNil(t, result.ExpiresAt)
	assert.Equal(t, wakeLeaseServiceTime().Add(2*time.Minute), *result.ExpiresAt)
	renewed, err := f.leases.Renew(t.Context(), key, owner.Holder, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, owner.Holder, renewed.Holder)
	require.NoError(t, f.store.InboxAck(t.Context(), key.AgentID, seq))
	released, err := f.leases.Release(t.Context(), key, owner.Holder, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "released", released.Outcome)
	assert.Zero(t, released.Pending)
	assert.Nil(t, released.ExpiresAt)
}

func TestWakeLeaseService_AbandonWithPending_DoesNotRearm(t *testing.T) {
	f := newWakeLeaseServiceFixture(t)
	seq := seedWakeLeaseForeignEvent(t, f)
	key := wakeLeaseServiceKey()
	owner, err := f.leases.Acquire(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.NoError(t, f.store.RecordInboxDelivery(t.Context(), key.AgentID, seq, "TEST-hook"))
	result, err := f.leases.Abandon(t.Context(), key, owner.Holder, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "abandoned", result.Outcome)
	assert.Equal(t, 1, result.Pending)
	assert.Nil(t, result.ExpiresAt)
	replacement, err := f.leases.Acquire(t.Context(), key, time.Minute)
	require.NoError(t, err)
	assert.NotEqual(t, owner.Holder, replacement.Holder)
	_, err = f.leases.Abandon(t.Context(), key, owner.Holder, time.Minute)
	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	renewed, err := f.leases.Renew(t.Context(), key, replacement.Holder, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, replacement.Holder, renewed.Holder)
}

func TestWakeLeaseService_OwnHolderAfterExpiry_RenewsExistingOwner(t *testing.T) {
	for _, delay := range []time.Duration{30 * time.Second, 2 * time.Minute} {
		t.Run(delay.String(), func(t *testing.T) {
			f := newWakeLeaseServiceFixture(t)
			owner, err := f.leases.Acquire(t.Context(), wakeLeaseServiceKey(), time.Minute)
			require.NoError(t, err)
			later := openWakeLeaseServiceFixture(t, f.path, wakeLeaseServiceTime().Add(delay))
			renewed, err := later.leases.Renew(t.Context(), wakeLeaseServiceKey(), owner.Holder, time.Minute)
			require.NoError(t, err)
			assert.Equal(t, owner.Holder, renewed.Holder)
			assert.Equal(t, owner.AcquiredAt, renewed.AcquiredAt)
			assert.Equal(t, owner.HighWater, renewed.HighWater)
			assert.Equal(t, wakeLeaseServiceTime().Add(delay+time.Minute), renewed.ExpiresAt)
		})
	}
}

func TestWakeLeaseService_ForeignTokenAfterExpiry_TakesOverWithNewOwner(t *testing.T) {
	f := newWakeLeaseServiceFixture(t)
	key := wakeLeaseServiceKey()
	owner, err := f.leases.Acquire(t.Context(), key, time.Minute)
	require.NoError(t, err)
	foreign := "0" + owner.Holder[1:]
	if foreign == owner.Holder {
		foreign = "1" + owner.Holder[1:]
	}
	later := openWakeLeaseServiceFixture(t, f.path, wakeLeaseServiceTime().Add(2*time.Minute))
	replacement, err := later.leases.Renew(t.Context(), key, foreign, time.Minute)
	require.NoError(t, err)
	assert.NotEqual(t, owner.Holder, replacement.Holder)
	assert.NotEqual(t, foreign, replacement.Holder)
	assert.Equal(t, wakeLeaseServiceTime().Add(2*time.Minute), replacement.AcquiredAt)
	assert.Equal(t, wakeLeaseServiceTime().Add(3*time.Minute), replacement.ExpiresAt)
}

func TestWakeLeaseService_ForeignHostOrUser_IndependentOwnership(t *testing.T) {
	for _, field := range []string{"host", "user"} {
		t.Run(field, func(t *testing.T) {
			f := newWakeLeaseServiceFixture(t)
			key := wakeLeaseServiceKey()
			first, err := f.leases.Acquire(t.Context(), key, time.Minute)
			require.NoError(t, err)
			if field == "host" {
				key.HostID = "other-host"
			} else {
				key.UID = "other-user"
			}
			second, err := f.leases.Acquire(t.Context(), key, time.Minute)
			require.NoError(t, err)
			assert.NotEqual(t, first.Holder, second.Holder)
		})
	}
}
