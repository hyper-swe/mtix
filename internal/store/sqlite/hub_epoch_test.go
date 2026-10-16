// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHubEpoch_LocalTransitions_StickyResetDistinctAndReopen(t *testing.T) {
	s, _ := applyTestStore(t)
	s.clock = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
	flipped, err := s.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	require.True(t, flipped)
	first, err := s.HubMark(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, first.Epoch)
	flipped, err = s.MarkHubConnected(t.Context(), "secrets")
	require.NoError(t, err)
	assert.False(t, flipped)
	same, err := s.HubMark(t.Context())
	require.NoError(t, err)
	assert.Equal(t, first, same)
	_, err = s.ResetHubMark(t.Context())
	require.NoError(t, err)
	absent, err := s.HubMark(t.Context())
	require.NoError(t, err)
	assert.Equal(t, HubMark{}, absent)
	_, err = s.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	second, err := s.HubMark(t.Context())
	require.NoError(t, err)
	assert.Equal(t, first.SetAt, second.SetAt)
	assert.NotEqual(t, first.Epoch, second.Epoch)
	reopened, err := New(s.dbPath, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	persisted, err := reopened.HubMark(t.Context())
	require.NoError(t, err)
	assert.Equal(t, second, persisted)
}

func TestHubEpoch_ForeignEvent_SameEpochAcrossReplicasAndDuplicateDoesNotMint(t *testing.T) {
	a, _ := applyTestStore(t)
	b, _ := applyTestStore(t)
	event := foreignCreate(t, "MTIX-1", 1)
	for _, s := range []*Store{a, b} {
		require.NoError(t, applyOnce(t, s, event))
	}
	first, err := a.HubMark(t.Context())
	require.NoError(t, err)
	second, err := b.HubMark(t.Context())
	require.NoError(t, err)
	assert.Equal(t, first.Epoch, second.Epoch)
	_, err = a.ResetHubMark(t.Context())
	require.NoError(t, err)
	require.NoError(t, applyOnce(t, a, event))
	absent, err := a.HubMark(t.Context())
	require.NoError(t, err)
	assert.False(t, absent.Set)
	assert.Empty(t, absent.Epoch)
	require.NoError(t, applyOnce(t, a, foreignCreate(t, "MTIX-2", 2)))
	next, err := a.HubMark(t.Context())
	require.NoError(t, err)
	assert.NotEqual(t, first.Epoch, next.Epoch)
}

func TestHubEpoch_MintFailure_RollsBackWholeMark(t *testing.T) {
	s, raw := applyTestStore(t)
	boom := errors.New("synthetic epoch mint failure")
	err := s.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := setHubMarkEpoch(t.Context(), tx, "config", time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), func() (uuid.UUID, error) { return uuid.Nil, boom })
		return err
	})
	require.ErrorIs(t, err, boom)
	var count int
	require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM board_flags WHERE key IN (?, ?)`, hubMarkKey, hubEpochKey).Scan(&count))
	assert.Zero(t, count)
}

func TestHubEpoch_PreEpochMark_StableFallbackAndResetClearsIt(t *testing.T) {
	s, raw := applyTestStore(t)
	_, err := s.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	_, err = raw.ExecContext(t.Context(), `DELETE FROM board_flags WHERE key = ?`, hubEpochKey)
	require.NoError(t, err)
	old, err := s.HubMark(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "legacy:"+old.Source+":"+old.SetAt, old.Epoch)
	_, err = s.ResetHubMark(t.Context())
	require.NoError(t, err)
	mark, err := s.HubMark(t.Context())
	require.NoError(t, err)
	assert.False(t, mark.Set)
	assert.Empty(t, mark.Epoch)
}

func TestHubEpoch_RejectedAndRolledBackForeignEvents_NeverMint(t *testing.T) {
	for _, mode := range []string{"invalid", "rollback", "own"} {
		t.Run(mode, func(t *testing.T) {
			s, raw := applyTestStore(t)
			event := foreignCreate(t, "MTIX-1", 1)
			if mode == "invalid" {
				event.Payload = []byte(`{"title":`)
			}
			if mode == "own" {
				require.NoError(t, applyOnce(t, s, event))
				_, err := s.ResetHubMark(t.Context())
				require.NoError(t, err)
				_, err = raw.ExecContext(t.Context(), `DELETE FROM applied_events WHERE event_id = ?`, event.EventID)
				require.NoError(t, err)
			}
			err := s.WithTx(context.Background(), func(tx *sql.Tx) error {
				if err := s.IdempotentApply(t.Context(), tx, event); err != nil {
					return err
				}
				if mode == "rollback" {
					return model.ErrConflict
				}
				return nil
			})
			if mode == "own" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			var count int
			require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM board_flags WHERE key = ?`, hubEpochKey).Scan(&count))
			assert.Zero(t, count)
		})
	}
}
