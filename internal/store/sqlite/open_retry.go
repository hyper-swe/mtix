// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlite3 "modernc.org/sqlite"

	"github.com/hyper-swe/mtix/internal/model"
)

// SQLite primary result codes that mean "try again": another connection holds
// a lock (BUSY, LOCKED) or changed the schema under a prepared statement
// (SCHEMA). None of them says anything about file integrity (MTIX-147).
const (
	sqliteBusy   = 5
	sqliteLocked = 6
	sqliteSchema = 17
)

// openRetry is the bounded retry policy for transient errors while a store
// is first opened. The sleeper is injected so tests never wait and no
// package-level state exists (MTIX-147).
type openRetry struct {
	attempts int
	base     time.Duration
	max      time.Duration
	sleep    func(ctx context.Context, d time.Duration) error
	// now is the injected clock. An attempt that already spent patience or
	// longer (busy_timeout waited inside SQLite) is not retried, so a store
	// that is genuinely locked still fails in about busy_timeout, not N times it.
	now      func() time.Time
	patience time.Duration
	// inject is a test seam: when set it is called at the start of each retried
	// step (stageInit, stageIntegrity) and may return an error to simulate a
	// transient failure. It is nil in production.
	inject func(stage string) error
}

// Names of the open steps that run under the retry policy.
const (
	stageInit      = "init"
	stageIntegrity = "integrity"
)

// step wraps fn so the injected fault, if any, runs first on every attempt.
func (rt openRetry) step(stage string, fn func() error) func() error {
	return func() error {
		if rt.inject != nil {
			if err := rt.inject(stage); err != nil {
				return err
			}
		}
		return fn()
	}
}

// defaultOpenRetry returns the production policy: 12 attempts, backoff
// doubling from 10ms to a 250ms cap (about 2.5s worst case in addition to
// busy_timeout), sleeping on a context-aware timer.
func defaultOpenRetry() openRetry {
	return openRetry{
		attempts: 12,
		base:     10 * time.Millisecond,
		max:      250 * time.Millisecond,
		now:      func() time.Time { return time.Now().UTC() },
		patience: time.Second,
		sleep: func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return fmt.Errorf("wait to retry store open: %w", ctx.Err())
			case <-timer.C:
				return nil
			}
		},
	}
}

// isTransientOpenError reports whether err is SQLITE_BUSY, SQLITE_LOCKED or
// SQLITE_SCHEMA. The driver's typed code is authoritative; message matching
// covers errors that crossed a layer as plain text.
func isTransientOpenError(err error) bool {
	if err == nil {
		return false
	}
	var sqErr *sqlite3.Error
	if errors.As(err, &sqErr) {
		switch sqErr.Code() & 0xFF {
		case sqliteBusy, sqliteLocked, sqliteSchema:
			return true
		}
	}
	msg := err.Error()
	for _, marker := range []string{"SQLITE_BUSY", "SQLITE_LOCKED", "SQLITE_SCHEMA",
		"database is locked", "schema has changed"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// retryTransient runs fn, retrying transient errors up to rt.attempts times
// in total with doubling, capped backoff. It returns the last error once the
// budget is spent, or immediately for a non-transient error.
func retryTransient(ctx context.Context, rt openRetry, fn func() error) error {
	delay := rt.base
	var err error
	for attempt := 1; attempt <= max(rt.attempts, 1); attempt++ {
		began := rt.now()
		if err = fn(); err == nil || !isTransientOpenError(err) {
			return err
		}
		if rt.now().Sub(began) >= rt.patience {
			return err
		}
		if attempt == max(rt.attempts, 1) {
			break
		}
		if sleepErr := rt.sleep(ctx, delay); sleepErr != nil {
			return sleepErr
		}
		delay = min(delay*2, rt.max)
	}
	return err
}

// checkIntegrity runs the quick_check callback under the retry policy.
// Only a real quick_check verdict or SQLITE_CORRUPT yields ErrCorrupted; a
// transient error that outlasts the budget yields ErrIntegrityCheckIncomplete
// so callers show a locked condition, never a corruption alarm (MTIX-147).
func checkIntegrity(ctx context.Context, rt openRetry, path string,
	check func(context.Context) (string, error)) error {
	var result string
	err := retryTransient(ctx, rt, rt.step(stageIntegrity, func() error {
		var cerr error
		result, cerr = check(ctx)
		return cerr
	}))
	switch {
	case err == nil && result == "ok":
		return nil
	case err == nil:
		return fmt.Errorf("integrity check on %s failed (%s); %s: %w",
			path, result, recoveryGuidance, model.ErrCorrupted)
	case isTransientOpenError(err):
		return fmt.Errorf("integrity check on %s could not run because the database stayed busy; "+
			"retry in a moment (this is not corruption): %w (%v)",
			path, model.ErrIntegrityCheckIncomplete, err)
	case isCorruptionError(err):
		return fmt.Errorf("integrity check on %s could not run; %s: %w (%v)",
			path, recoveryGuidance, model.ErrCorrupted, err)
	default:
		return fmt.Errorf("integrity check on %s could not run (%s); retry, and check for "+
			"other processes or storage problems (this is not corruption): %w (%v)",
			path, describeIntegrityCause(err), model.ErrIntegrityCheckIncomplete, err)
	}
}

// describeIntegrityCause names, in words, why an integrity check could not run.
func describeIntegrityCause(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	case strings.Contains(msg, "SQLITE_IOERR"), strings.Contains(msg, "I/O error"):
		return "I/O error"
	case strings.Contains(msg, "SQLITE_NOMEM"), strings.Contains(msg, "out of memory"):
		return "out of memory"
	case strings.Contains(msg, "SQLITE_CANTOPEN"), strings.Contains(msg, "unable to open"):
		return "cannot open the database file"
	default:
		return msg
	}
}
