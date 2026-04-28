// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// RetryConfig controls the exponential-backoff loop used by PushEvents
// and PullEvents per FR-18.3 (transient error retry).
type RetryConfig struct {
	MaxAttempts int           // total attempts including the first
	Base        time.Duration // first delay
	Factor      float64       // exponent base; usually 2
	Max         time.Duration // delay cap
	Jitter      time.Duration // additive jitter ceiling (set 0 in tests)
}

// DefaultRetryConfig is the production setting per the design.
// Base 200ms, factor 2, cap 10s, max 5 attempts; jitter at runtime.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 5,
		Base:        200 * time.Millisecond,
		Factor:      2.0,
		Max:         10 * time.Second,
		Jitter:      50 * time.Millisecond,
	}
}

// retryWithBackoff runs op until it succeeds, MaxAttempts is exhausted,
// or op returns an error that is NOT classified as transient. Returns
// the last error wrapped with attempt count for diagnosis.
//
// Sleeping between attempts respects ctx — if ctx is canceled mid-sleep,
// the function returns ctx.Err() immediately.
func retryWithBackoff(ctx context.Context, cfg RetryConfig, op func(ctx context.Context) error) error {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}
	delay := cfg.Base
	var lastErr error
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		err := op(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransient(err) {
			return fmt.Errorf("attempt %d permanent: %w", attempt, err)
		}
		if attempt == cfg.MaxAttempts {
			break
		}
		// Sleep with ctx awareness.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay + jitter(cfg.Jitter, attempt)):
		}
		delay = nextDelay(delay, cfg)
	}
	return fmt.Errorf("after %d attempts: %w", cfg.MaxAttempts, lastErr)
}

// nextDelay computes the next exponential delay, clamped at cfg.Max.
func nextDelay(cur time.Duration, cfg RetryConfig) time.Duration {
	next := time.Duration(float64(cur) * cfg.Factor)
	if next > cfg.Max {
		return cfg.Max
	}
	return next
}

// jitter returns a deterministic-looking but cheap pseudo-random
// addend in [0, ceiling). Cheap because we don't want crypto/rand on
// the retry hot path; deterministic enough for testing because
// callers can pass Jitter=0 to disable.
func jitter(ceiling time.Duration, attempt int) time.Duration {
	if ceiling <= 0 {
		return 0
	}
	// Simple linear congruential mix; not cryptographic.
	mix := int64(attempt)*1103515245 + 12345
	return time.Duration(mix%int64(ceiling)) * time.Nanosecond
}

// isTransient classifies err for the retry loop. Transient =
// retry-worthy (network timeout, server-side connection failure,
// pgx ErrTxClosed during a flaky pool, statement_timeout). Permanent
// = abort immediately (auth failure, bad SQL, schema mismatch,
// validation rejection).
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	// Context cancellation/deadline are NOT transient — caller asked
	// to stop. Return false so retryWithBackoff bubbles them up
	// without further sleeps.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, pgx.ErrTxClosed) {
		return true
	}
	// pgx PG-side errors carry an SQLSTATE.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "57014": // statement_timeout
			return true
		case "08000", "08003", "08006", "08001", "08004": // connection exception family
			return true
		case "40001", "40P01": // serialization_failure, deadlock_detected
			return true
		default:
			return false
		}
	}
	// Network errors (Temporary or Timeout) are transient.
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true
		}
	}
	// String-based fallback for cases pgx wraps as plain errors.
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") {
		return true
	}
	return false
}
