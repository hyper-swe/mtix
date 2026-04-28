// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// retry tests are in the package (not _test) so we can exercise the
// unexported retryWithBackoff and isTransient directly.

func TestDefaultRetryConfig_Shape(t *testing.T) {
	c := DefaultRetryConfig()
	require.Equal(t, 5, c.MaxAttempts)
	require.Equal(t, 200*time.Millisecond, c.Base)
	require.Equal(t, 2.0, c.Factor)
	require.Equal(t, 10*time.Second, c.Max)
	require.Equal(t, 50*time.Millisecond, c.Jitter)
}

func TestRetry_FirstAttemptSuccess(t *testing.T) {
	calls := 0
	err := retryWithBackoff(context.Background(), RetryConfig{MaxAttempts: 3, Base: time.Millisecond}, func(_ context.Context) error {
		calls++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls, "no retry on first-attempt success")
}

func TestRetry_RetriesTransientThenSucceeds(t *testing.T) {
	calls := 0
	err := retryWithBackoff(context.Background(), RetryConfig{
		MaxAttempts: 5, Base: time.Microsecond, Factor: 2, Max: time.Millisecond,
	}, func(_ context.Context) error {
		calls++
		if calls < 3 {
			return pgx.ErrTxClosed
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, calls, "retried 2 transient errors then succeeded")
}

func TestRetry_FailsImmediatelyOnPermanent(t *testing.T) {
	calls := 0
	permErr := errors.New("auth failed")
	err := retryWithBackoff(context.Background(), DefaultRetryConfig(), func(_ context.Context) error {
		calls++
		return permErr
	})
	require.Error(t, err)
	require.Equal(t, 1, calls, "no retry on permanent error")
	require.ErrorIs(t, err, permErr)
}

func TestRetry_ExhaustsAttemptsOnPersistentTransient(t *testing.T) {
	calls := 0
	err := retryWithBackoff(context.Background(), RetryConfig{
		MaxAttempts: 3, Base: time.Microsecond, Factor: 2, Max: time.Millisecond,
	}, func(_ context.Context) error {
		calls++
		return pgx.ErrTxClosed
	})
	require.Error(t, err)
	require.Equal(t, 3, calls)
	require.Contains(t, err.Error(), "after 3 attempts")
}

func TestRetry_ContextCancelMidSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := retryWithBackoff(ctx, RetryConfig{
		MaxAttempts: 10, Base: 100 * time.Millisecond, Factor: 2, Max: time.Second,
	}, func(_ context.Context) error {
		calls++
		return pgx.ErrTxClosed
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled))
}

func TestRetry_ContextDeadlineNotTransient(t *testing.T) {
	calls := 0
	err := retryWithBackoff(context.Background(), DefaultRetryConfig(), func(_ context.Context) error {
		calls++
		return context.DeadlineExceeded
	})
	require.Error(t, err)
	require.Equal(t, 1, calls,
		"context.DeadlineExceeded must NOT be retried — caller's deadline is final")
}

func TestRetry_ZeroMaxAttemptsMeansOne(t *testing.T) {
	calls := 0
	_ = retryWithBackoff(context.Background(), RetryConfig{MaxAttempts: 0}, func(_ context.Context) error {
		calls++
		return errors.New("oops")
	})
	require.Equal(t, 1, calls, "MaxAttempts <= 0 normalizes to 1")
}

func TestNextDelay_CapsAtMax(t *testing.T) {
	c := RetryConfig{Factor: 10, Max: 100 * time.Millisecond}
	require.Equal(t, 100*time.Millisecond, nextDelay(50*time.Millisecond, c))
	require.Equal(t, 100*time.Millisecond, nextDelay(200*time.Millisecond, c))
}

func TestIsTransient_PgxErrTxClosed(t *testing.T) {
	require.True(t, isTransient(pgx.ErrTxClosed))
}

func TestIsTransient_PgErrorStatementTimeout(t *testing.T) {
	require.True(t, isTransient(&pgconn.PgError{Code: "57014"}))
}

func TestIsTransient_PgErrorConnectionFamily(t *testing.T) {
	for _, code := range []string{"08000", "08003", "08006", "08001", "08004"} {
		require.True(t, isTransient(&pgconn.PgError{Code: code}), "code %s must be transient", code)
	}
}

func TestIsTransient_PgErrorSerializationAndDeadlock(t *testing.T) {
	require.True(t, isTransient(&pgconn.PgError{Code: "40001"}))
	require.True(t, isTransient(&pgconn.PgError{Code: "40P01"}))
}

func TestIsTransient_PgErrorAuthIsPermanent(t *testing.T) {
	require.False(t, isTransient(&pgconn.PgError{Code: "28000"}),
		"invalid_authorization_specification is NEVER retried")
	require.False(t, isTransient(&pgconn.PgError{Code: "42P01"}),
		"undefined_table (schema mismatch) is NEVER retried")
}

func TestIsTransient_NetworkTimeout(t *testing.T) {
	te := &timeoutErr{}
	require.True(t, isTransient(te))
}

func TestIsTransient_StringFallback(t *testing.T) {
	cases := []string{
		"connection reset by peer",
		"broken pipe",
		"connection refused",
		"BROKEN PIPE", // case-insensitive
	}
	for _, msg := range cases {
		require.True(t, isTransient(errors.New(msg)), "%q must be transient", msg)
	}
}

func TestIsTransient_OtherErrorIsPermanent(t *testing.T) {
	require.False(t, isTransient(errors.New("unrelated business failure")))
}

func TestIsTransient_NilIsNotTransient(t *testing.T) {
	require.False(t, isTransient(nil))
}

func TestIsTransient_ContextCancellationIsPermanent(t *testing.T) {
	require.False(t, isTransient(context.Canceled))
	require.False(t, isTransient(context.DeadlineExceeded))
}

// timeoutErr satisfies net.Error with Timeout()=true.
type timeoutErr struct{}

func (e *timeoutErr) Error() string   { return "i/o timeout" }
func (e *timeoutErr) Timeout() bool   { return true }
func (e *timeoutErr) Temporary() bool { return true }

// Compile-time check.
var _ net.Error = (*timeoutErr)(nil)
