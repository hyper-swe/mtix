// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// applyFaultStore fails the apply of one chosen event, failures times, and
// then behaves like the real store. It is how a transient store error (busy,
// full disk, cancelled context) is injected at the exact point MTIX-303
// describes: inside Store.WithTx / ApplyRelayEvent for a valid record.
type applyFaultStore struct {
	*fakeStore
	target   string // event id whose apply fails
	err      error  // returned from ApplyRelayEvent
	failures int    // how many applies of target fail; <0 means always
	cancel   func() // when set, called before err is returned (cancelled context)
}

func (f *applyFaultStore) ApplyRelayEvent(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	if e.EventID == f.target && f.failures != 0 {
		if f.failures > 0 {
			f.failures--
		}
		if f.cancel != nil {
			f.cancel()
		}
		return f.err
	}
	return f.fakeStore.ApplyRelayEvent(ctx, tx, e)
}

// realSQLiteError returns an error the driver itself produced: a constraint
// violation (a verdict about the data) or a read-only write (a store fault).
func realSQLiteError(t *testing.T, readOnly bool) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE probe (a INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO probe (a) VALUES (1)`)
	require.NoError(t, err)
	if !readOnly {
		_, err = db.Exec(`INSERT INTO probe (a) VALUES (1)`)
		require.Error(t, err)
		return err
	}
	ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ro.Close() })
	_, err = ro.Exec(`INSERT INTO probe (a) VALUES (2)`)
	require.Error(t, err)
	return err
}

func (r *peerRig) quarantineRows(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, r.store.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM relay_quarantine`).Scan(&n))
	return n
}

// publishThree publishes three valid creates and returns them.
func publishThree(t *testing.T, r *peerRig, key []byte) [3]model.SyncEvent {
	t.Helper()
	var evs [3]model.SyncEvent
	for i := range evs {
		evs[i] = event(t, model.OpCreateNode, fmt.Sprintf("PROJ-%d", i+1), int64(i+1),
			&model.CreateNodePayload{Title: fmt.Sprintf("task %d", i+1)})
	}
	r.publish(t, key, marshalEvents(t, evs[0], evs[1], evs[2])...)
	return evs
}

type storeFault struct {
	name    string
	err     error
	latches bool // the real store fail-stops on this fault until reopened
}

func storeFaults(t *testing.T) []storeFault {
	return []storeFault{
		{"context cancelled", fmt.Errorf("apply: %w", context.Canceled), false},
		{"deadline exceeded", fmt.Errorf("apply: %w", context.DeadlineExceeded), false},
		{"connection done", fmt.Errorf("apply: dedupe check: %w", sql.ErrConnDone), false},
		{"transaction done", fmt.Errorf("apply: %w", sql.ErrTxDone), false},
		{"bad connection", fmt.Errorf("apply: %w", driver.ErrBadConn), false},
		{"disk full", fmt.Errorf("apply: mirror: %w", &fs.PathError{Op: "write", Path: "wal", Err: syscall.ENOSPC}), true},
		{"read-only filesystem", fmt.Errorf("apply: mirror: %w", &fs.PathError{Op: "write", Path: "wal", Err: syscall.EROFS}), false},
		{"permission denied", fmt.Errorf("apply: mirror: %w", &fs.PathError{Op: "open", Path: "db", Err: syscall.EACCES}), false},
		{"model disk full", fmt.Errorf("apply: %w", model.ErrDiskFull), false},
		{"model corrupted", fmt.Errorf("apply: %w", model.ErrCorrupted), false},
		{"sqlite read-only database", fmt.Errorf("apply: record applied: %w", realSQLiteError(t, true)), false},
	}
}

// A store error during apply must not quarantine the valid record, must not
// move the cursor past it, must fail the poll, and the next healthy poll must
// apply it exactly once (MTIX-303).
func TestRelayIngest_StoreErrorDuringApply_KeepsCursorAndRetriesOnce(t *testing.T) {
	for _, tt := range storeFaults(t) {
		t.Run(tt.name, func(t *testing.T) {
			r := newPeerRig(t)
			evs := publishThree(t, r, fleetKey)
			fault := &applyFaultStore{
				fakeStore: &fakeStore{inner: r.store}, target: evs[1].EventID, err: tt.err, failures: 1,
			}
			cfg := r.config()
			cfg.Store = fault
			in, err := ingest.New(cfg)
			require.NoError(t, err)

			stats, err := in.IngestAll(context.Background())
			require.ErrorIs(t, err, tt.err, "a store failure fails the poll")
			require.Zero(t, stats.Quarantined)
			require.Empty(t, stats.Quarantines)
			require.Zero(t, r.quarantineRows(t), "a valid record is never stored as quarantined")
			require.Equal(t, 1, stats.Applied, "only the record before the failure applied")
			require.Equal(t, uint64(1), r.cursor(t).RS, "the cursor stays before the failed record")
			require.Equal(t, 1, r.journalCount(t))

			if tt.latches {
				// A full disk latches the real store into fail-stop until
				// the process restarts (NFR-2.8): reopen it as an operator
				// would after freeing space.
				require.NoError(t, r.store.Close())
				reopened, openErr := sqlite.New(r.dbPath, slog.Default())
				require.NoError(t, openErr)
				t.Cleanup(func() { _ = reopened.Close() })
				r.store = reopened
				fault.inner = reopened
				fault.failures = 0
				cfg.Store = fault
				in, err = ingest.New(cfg)
				require.NoError(t, err)
			}
			stats, err = in.IngestAll(context.Background())
			require.NoError(t, err)
			require.Equal(t, 2, stats.Applied, "the failed record and the one after it apply")
			require.Zero(t, stats.Quarantined)
			require.Equal(t, uint64(3), r.cursor(t).RS)
			require.Equal(t, 3, r.journalCount(t), "each event is in the journal exactly once")

			stats, err = in.IngestAll(context.Background())
			require.NoError(t, err)
			require.Zero(t, stats.Applied, "a further poll applies nothing twice")
			require.Equal(t, 3, r.journalCount(t))
		})
	}
}

// A begin or commit failure of the transaction itself (the apply function
// never ran, or ran and the commit failed) is a store error whatever its text.
func TestRelayIngest_TransactionFailure_KeepsCursorAndRetriesOnce(t *testing.T) {
	r := newPeerRig(t)
	publishThree(t, r, fleetKey)
	boom := errors.New("database is locked")
	cfg := r.withFake(&fakeStore{errTx: boom})
	in, err := ingest.New(cfg)
	require.NoError(t, err)

	stats, err := in.IngestAll(context.Background())
	require.ErrorIs(t, err, boom)
	require.Zero(t, stats.Quarantined)
	require.Zero(t, r.quarantineRows(t))
	require.Zero(t, r.cursor(t).RS)

	healthy, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err = healthy.IngestAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, stats.Applied)
	require.Equal(t, 3, r.journalCount(t))
}

// A cancelled poll whose apply error carries no context sentinel (the driver
// reports the interrupt in its own words) is still not a verdict about the
// record.
func TestRelayIngest_CancelledPollWithOpaqueApplyError_IsNotQuarantined(t *testing.T) {
	r := newPeerRig(t)
	evs := publishThree(t, r, fleetKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opaque := errors.New("interrupted")
	fault := &applyFaultStore{
		fakeStore: &fakeStore{inner: r.store}, target: evs[0].EventID, err: opaque, failures: 1, cancel: cancel,
	}
	cfg := r.config()
	cfg.Store = fault
	in, err := ingest.New(cfg)
	require.NoError(t, err)

	stats, err := in.IngestAll(ctx)
	require.Error(t, err)
	require.Zero(t, stats.Quarantined)
	require.Zero(t, r.quarantineRows(t))
	require.Zero(t, r.cursor(t).RS)

	healthy, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err = healthy.IngestAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, stats.Applied)
}

// The unauthenticated relay has its own refusal branch (a stall); a store
// error must not surface as a stall of that peer either.
func TestRelayIngest_StoreErrorDuringApply_UnauthenticatedRelayFailsPollNoStall(t *testing.T) {
	r := newPeerRig(t)
	evs := publishThree(t, r, nil)
	boom := fmt.Errorf("apply: %w", sql.ErrConnDone)
	fault := &applyFaultStore{
		fakeStore: &fakeStore{inner: r.store}, target: evs[1].EventID, err: boom, failures: 1,
	}
	cfg := r.config()
	cfg.Keys = nil
	cfg.Unauthenticated = true
	cfg.Store = fault
	in, err := ingest.New(cfg)
	require.NoError(t, err)

	stats, err := in.IngestAll(context.Background())
	require.ErrorIs(t, err, boom)
	require.Empty(t, stats.Stalls, "a local store failure is not a stall of the peer's stream")
	require.Equal(t, uint64(1), r.cursor(t).RS)

	stats, err = in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, stats.Applied)
	require.Equal(t, 3, r.journalCount(t))
}

// The uid check reads the store; a failed read is not a uid conflict and
// must not quarantine the record.
func TestRelayIngest_UIDLookupStoreFailure_KeepsCursorAndRetriesOnce(t *testing.T) {
	r := newPeerRig(t)
	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"})
	e.UID = "01a20a06-b461-7530-96c8-944c438ca737"
	r.publish(t, fleetKey, marshalEvents(t, e)...)
	boom := errors.New("uid lookup unavailable")

	in, err := ingest.New(r.withFake(&fakeStore{errResolveBy: boom}))
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.ErrorIs(t, err, boom)
	require.Zero(t, stats.Quarantined)
	require.Zero(t, r.quarantineRows(t))
	require.Zero(t, r.cursor(t).RS)

	healthy, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err = healthy.IngestAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, stats.Applied)
	require.Equal(t, 1, r.journalCount(t))
}

// Records the apply path genuinely rejects still quarantine and the stream
// moves on: the fix separates verdicts from store faults, it does not make
// every apply error a stall.
func TestRelayIngest_ApplyVerdicts_StillQuarantine(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"invalid input", fmt.Errorf("apply: %w", model.ErrInvalidInput)},
		{"not found", fmt.Errorf("apply: parent: %w", model.ErrNotFound)},
		{"conflict", fmt.Errorf("apply: %w", model.ErrConflict)},
		{"already exists", fmt.Errorf("apply: %w", model.ErrAlreadyExists)},
		{"undecodable payload", errors.New("apply create_node: decode payload: invalid character")},
		{"sqlite constraint violation", fmt.Errorf("apply: %w", realSQLiteError(t, false))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newPeerRig(t)
			evs := publishThree(t, r, fleetKey)
			fault := &applyFaultStore{
				fakeStore: &fakeStore{inner: r.store}, target: evs[1].EventID, err: tt.err, failures: -1,
			}
			cfg := r.config()
			cfg.Store = fault
			in, err := ingest.New(cfg)
			require.NoError(t, err)

			stats, err := in.IngestAll(context.Background())
			require.NoError(t, err)
			require.Equal(t, 1, stats.Quarantined)
			require.Equal(t, 2, stats.Applied)
			require.Equal(t, 1, r.quarantineRows(t))
			require.Equal(t, uint64(3), r.cursor(t).RS)
		})
	}
}
