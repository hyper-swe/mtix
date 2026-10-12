// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// recordingRetry returns a retry policy whose sleeper records delays instead of sleeping.
func recordingRetry(attempts int) (openRetry, *[]time.Duration) {
	var slept []time.Duration
	return openRetry{
		attempts: attempts,
		base:     10 * time.Millisecond,
		max:      40 * time.Millisecond,
		now:      func() time.Time { return time.Unix(0, 0) },
		patience: time.Second,
		sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	}, &slept
}

func TestIsTransientOpenError_Classification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"busy", errors.New("enable WAL: database is locked (5) (SQLITE_BUSY)"), true},
		{"locked", errors.New("database table is locked (6) (SQLITE_LOCKED)"), true},
		{"schema changed", errors.New("SQL logic error: schema has changed: nodes_fts (17) (SQLITE_SCHEMA)"), true},
		{"corrupt", errors.New("database disk image is malformed (11) (SQLITE_CORRUPT)"), false},
		{"nil", nil, false},
		{"other", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTransientOpenError(tt.err))
		})
	}
}

func TestIsCorruptionError_TransientCodes_NotCorruption(t *testing.T) {
	for _, msg := range []string{
		"schema has changed: nodes_fts (17) (SQLITE_SCHEMA)",
		"database is locked (5) (SQLITE_BUSY)",
		"database table is locked (6) (SQLITE_LOCKED)",
	} {
		assert.False(t, isCorruptionError(errors.New(msg)), msg)
	}
	assert.True(t, isCorruptionError(errors.New("database disk image is malformed")))
}

func TestRetryTransient_BusyThenOK_RetriesWithBoundedBackoff(t *testing.T) {
	rt, slept := recordingRetry(6)
	calls := 0
	err := retryTransient(context.Background(), rt, func() error {
		calls++
		if calls < 5 {
			return errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 5, calls)
	assert.Equal(t, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond,
		40 * time.Millisecond, 40 * time.Millisecond}, *slept)
}

func TestRetryTransient_PersistentBusy_ReturnsLastErrorAfterBudget(t *testing.T) {
	rt, slept := recordingRetry(3)
	calls := 0
	err := retryTransient(context.Background(), rt, func() error {
		calls++
		return fmt.Errorf("attempt %d: database is locked (5) (SQLITE_BUSY)", calls)
	})
	require.Error(t, err)
	assert.Equal(t, 3, calls)
	assert.Len(t, *slept, 2)
	assert.Contains(t, err.Error(), "attempt 3")
}

func TestRetryTransient_NonTransient_NotRetried(t *testing.T) {
	rt, slept := recordingRetry(5)
	calls := 0
	boom := errors.New("boom")
	err := retryTransient(context.Background(), rt, func() error { calls++; return boom })
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 1, calls)
	assert.Empty(t, *slept)
}

func TestRetryTransient_SleeperError_Propagates(t *testing.T) {
	rt, _ := recordingRetry(5)
	stop := errors.New("cancelled")
	rt.sleep = func(context.Context, time.Duration) error { return stop }
	err := retryTransient(context.Background(), rt, func() error {
		return errors.New("SQLITE_BUSY")
	})
	assert.ErrorIs(t, err, stop)
}

func TestDefaultOpenRetry_IsBoundedAndSleeps(t *testing.T) {
	rt := defaultOpenRetry()
	assert.Positive(t, rt.attempts)
	assert.LessOrEqual(t, rt.attempts, 20)
	require.NoError(t, rt.sleep(context.Background(), time.Microsecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Error(t, rt.sleep(ctx, time.Hour))
}

func TestCheckIntegrity_SchemaThenOK_RetriesAndPasses(t *testing.T) {
	rt, slept := recordingRetry(4)
	calls := 0
	err := checkIntegrity(context.Background(), rt, "x.db", func(context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("SQL logic error: schema has changed: nodes_fts (17) (SQLITE_SCHEMA)")
		}
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Len(t, *slept, 2)
}

func TestCheckIntegrity_PersistentTransient_NotCorruption(t *testing.T) {
	for _, msg := range []string{"database is locked (5) (SQLITE_BUSY)",
		"schema has changed (17) (SQLITE_SCHEMA)", "table is locked (6) (SQLITE_LOCKED)"} {
		rt, _ := recordingRetry(3)
		err := checkIntegrity(context.Background(), rt, "x.db", func(context.Context) (string, error) {
			return "", errors.New(msg)
		})
		require.Error(t, err, msg)
		assert.ErrorIs(t, err, model.ErrIntegrityCheckIncomplete, msg)
		assert.NotErrorIs(t, err, model.ErrCorrupted, msg)
	}
}

func TestCheckIntegrity_RealFailures_AreCorruption(t *testing.T) {
	rt, _ := recordingRetry(3)
	err := checkIntegrity(context.Background(), rt, "x.db", func(context.Context) (string, error) {
		return "row 1 missing from index", nil
	})
	assert.ErrorIs(t, err, model.ErrCorrupted)
	err = checkIntegrity(context.Background(), rt, "x.db", func(context.Context) (string, error) {
		return "", errors.New("database disk image is malformed (11) (SQLITE_CORRUPT)")
	})
	assert.ErrorIs(t, err, model.ErrCorrupted)
}

func TestOpenDBWith_WALSwitch_UsesInjectedRetry(t *testing.T) {
	rt, slept := recordingRetry(3)
	db, err := openDBWith(context.Background(), filepath.Join(t.TempDir(), "s.db"), true, false, rt)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	assert.Empty(t, *slept, "uncontended open must not back off")
}

func TestRetryTransient_SlowAttempt_NotRetried(t *testing.T) {
	rt, slept := recordingRetry(5)
	clock := time.Unix(0, 0)
	rt.now = func() time.Time { return clock }
	calls := 0
	err := retryTransient(context.Background(), rt, func() error {
		calls++
		clock = clock.Add(5 * time.Second) // busy_timeout already waited
		return errors.New("database is locked (5) (SQLITE_BUSY)")
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls)
	assert.Empty(t, *slept)
}
