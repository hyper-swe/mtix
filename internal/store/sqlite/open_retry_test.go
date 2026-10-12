// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sqlite3 "modernc.org/sqlite"

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
		{"locked", errors.New("database table is locked (6)"), true},
		{"schema changed", errors.New("SQL logic error: schema has changed: nodes_fts (1)"), true},
		{"schema changed (17)", errors.New("database schema has changed (17)"), true},
		{"corrupt", errors.New("database disk image is malformed (11)"), false},
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
		"SQL logic error: schema has changed: nodes_fts (1)", "database schema has changed (17)",
		"database is locked (5) (SQLITE_BUSY)",
		"database table is locked (6)",
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
			return "", errors.New("SQL logic error: schema has changed: nodes_fts (1)")
		}
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Len(t, *slept, 2)
}

func TestCheckIntegrity_PersistentTransient_NotCorruption(t *testing.T) {
	for _, msg := range []string{"database is locked (5) (SQLITE_BUSY)",
		"database schema has changed (17)", "database table is locked: t (6)"} {
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
		return "", errors.New("database disk image is malformed (11)")
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

// opaqueErr hides the driver's message so only the typed error code can classify it.
type opaqueErr struct{ err error }

func (e opaqueErr) Error() string { return "opaque failure" }
func (e opaqueErr) Unwrap() error { return e.err }

// TestIsTransientOpenError_RealDriverBusy_ClassifiedByTypedCode takes a genuine
// SQLITE_BUSY from a really locked database and classifies it with the message
// hidden, so the typed-code path (not the text fallback) is what is exercised.
func TestIsTransientOpenError_RealDriverBusy_ClassifiedByTypedCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	holder, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, holder.Close()) }()
	holder.SetMaxOpenConns(1)
	_, err = holder.Exec("CREATE TABLE t(x)")
	require.NoError(t, err)
	tx, err := holder.Begin()
	require.NoError(t, err)
	_, err = tx.Exec("INSERT INTO t VALUES (1)")
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	other, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(1)")
	require.NoError(t, err)
	defer func() { require.NoError(t, other.Close()) }()
	_, err = other.Exec("INSERT INTO t VALUES (2)")
	require.Error(t, err)

	var sqErr *sqlite3.Error
	require.ErrorAs(t, err, &sqErr)
	assert.Equal(t, sqliteBusy, sqErr.Code()&0xFF)
	assert.True(t, isTransientOpenError(opaqueErr{err: err}))
	assert.False(t, isCorruptionError(err))
}

func TestRetryTransient_AttemptTakingExactlyPatience_NotRetried(t *testing.T) {
	rt, slept := recordingRetry(5)
	clock := time.Unix(0, 0)
	rt.now = func() time.Time { return clock }
	calls := 0
	err := retryTransient(context.Background(), rt, func() error {
		calls++
		clock = clock.Add(rt.patience)
		return errors.New("SQLITE_BUSY")
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls)
	assert.Empty(t, *slept)
}

func TestCheckIntegrity_NonTransientFailures_AreIncompleteNotCorruption(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		cause string
	}{
		{"io error", errors.New("disk I/O error (10)"), "(I/O error)"},
		{"out of memory", errors.New("out of memory (7)"), "(out of memory)"},
		{"cannot open", errors.New("unable to open database file (14)"), "(cannot open the database file)"},
		{"cancelled", context.Canceled, "(cancelled)"},
		{"deadline", context.DeadlineExceeded, "(cancelled)"},
		{"unknown", errors.New("something odd"), "something odd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, _ := recordingRetry(3)
			err := checkIntegrity(context.Background(), rt, "x.db", func(context.Context) (string, error) {
				return "", tt.err
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, model.ErrIntegrityCheckIncomplete)
			assert.NotErrorIs(t, err, model.ErrCorrupted)
			assert.Contains(t, err.Error(), tt.cause)
		})
	}
}

func TestCheckIntegrity_CorruptionSignals_AreCorrupted(t *testing.T) {
	for _, msg := range []string{"database disk image is malformed (11)", "SQLITE_CORRUPT"} {
		rt, _ := recordingRetry(3)
		err := checkIntegrity(context.Background(), rt, "x.db", func(context.Context) (string, error) {
			return "", errors.New(msg)
		})
		assert.ErrorIs(t, err, model.ErrCorrupted, msg)
		assert.NotErrorIs(t, err, model.ErrIntegrityCheckIncomplete, msg)
	}
}

// errSchemaChanged is the driver's real SQLITE_SCHEMA-class text from the
// ticket's false corruption alarm (modernc 1.35.0 formats errors as
// "<errstr>: <errmsg> (<rc>)" and adds a name suffix only for BUSY).
var errSchemaChanged = errors.New("SQL logic error: schema has changed: nodes_fts (1)")

// failFirst injects errSchemaChanged at one stage for the first n calls.
func failFirst(stage string, n int, calls *int) func(string) error {
	return func(s string) error {
		if s != stage {
			return nil
		}
		*calls++
		if *calls <= n {
			return errSchemaChanged
		}
		return nil
	}
}

// TestNewWithRetry_SchemaChangedAtEachStage_RetriedSeparately: the init retry and
// the integrity-check retry each must absorb SQLITE_SCHEMA on their own.
func TestNewWithRetry_SchemaChangedAtEachStage_RetriedSeparately(t *testing.T) {
	for _, stage := range []string{stageInit, stageIntegrity} {
		t.Run(stage, func(t *testing.T) {
			rt, slept := recordingRetry(4)
			calls := 0
			rt.inject = failFirst(stage, 3, &calls)
			s, err := newWithRetry(filepath.Join(t.TempDir(), "s.db"), slog.New(slog.DiscardHandler), rt)
			require.NoError(t, err)
			require.NoError(t, s.Close())
			assert.Equal(t, 4, calls)
			assert.Len(t, *slept, 3)
		})
	}
}

func TestNewWithRetry_PersistentSchemaChange_IsNeverCorruption(t *testing.T) {
	for _, stage := range []string{stageInit, stageIntegrity} {
		t.Run(stage, func(t *testing.T) {
			rt, _ := recordingRetry(3)
			calls := 0
			rt.inject = failFirst(stage, 1000, &calls)
			_, err := newWithRetry(filepath.Join(t.TempDir(), "s.db"), slog.New(slog.DiscardHandler), rt)
			require.Error(t, err)
			assert.NotErrorIs(t, err, model.ErrCorrupted)
			assert.Equal(t, 3, calls)
		})
	}
}

// A real SQLITE_LOCKED cannot be produced in a test: modernc blocks on
// unlock_notify for a shared-cache table lock instead of returning it. The
// LOCKED class is therefore classified by its real text ("database table is
// locked (6)", see TestIsTransientOpenError_Classification), and its typed
// code case in isTransientOpenError has no real-error test.

// TestIsTransientOpenError_RealDriverBusySnapshot_ClassifiedByPrimaryCode takes a
// genuine SQLITE_BUSY_SNAPSHOT (WAL read then write conflict). Whatever code the
// driver reports, the primary code must classify it as transient.
func TestIsTransientOpenError_RealDriverBusySnapshot_ClassifiedByPrimaryCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(1)")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	_, err = db.Exec("CREATE TABLE t(x)")
	require.NoError(t, err)
	ctx := context.Background()
	c1, err := db.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = c1.Close() }()
	c2, err := db.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()
	_, err = c1.ExecContext(ctx, "BEGIN")
	require.NoError(t, err)
	var n int
	require.NoError(t, c1.QueryRowContext(ctx, "SELECT count(*) FROM t").Scan(&n))
	_, err = c2.ExecContext(ctx, "INSERT INTO t VALUES (1)")
	require.NoError(t, err)
	_, err = c1.ExecContext(ctx, "INSERT INTO t VALUES (2)")
	require.Error(t, err)
	var sqErr *sqlite3.Error
	require.ErrorAs(t, err, &sqErr)
	assert.Equal(t, sqliteBusy, sqErr.Code()&0xFF, "code %d", sqErr.Code())
	assert.True(t, isTransientOpenError(opaqueErr{err: err}))
}

// TestNew_DamagedHeader_ReportsCorruptionNotCouldNotRun drives a real damaged
// database through New. A genuine SQLITE_CORRUPT must read as corruption and
// carry the recovery path, and must never read like the transient
// "integrity check ... could not run" message the runbook says to retry.
func TestNew_DamagedHeader_ReportsCorruptionNotCouldNotRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := New(path, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, s.Close())

	f, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // test-owned temp file
	require.NoError(t, err)
	_, err = f.WriteAt([]byte{200}, 20) // header byte 20: reserved space per page
	require.NoError(t, err)
	require.NoError(t, f.Close())

	_, err = New(path, slog.New(slog.DiscardHandler))
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrCorrupted)
	assert.NotErrorIs(t, err, model.ErrIntegrityCheckIncomplete)
	assert.NotContains(t, err.Error(), "could not run")
	assert.Contains(t, err.Error(), "corruption")
	assert.Contains(t, err.Error(), "mtix recover")
}
