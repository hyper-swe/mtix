// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// hubIDLookupChunk bounds the ids one hub lookup carries.
const hubIDLookupChunk = 500

// enrolHub is the hub surface the enrolment uses.
type enrolHub interface {
	pullSweepHub
}

// hubDialer connects to the hub with the worktree's own secrets and returns
// the connection with the function that closes it. Each port dials for itself,
// so a hub that changes between two ports is seen by the second.
type hubDialer func(ctx context.Context, root string) (enrolHub, func(), error)

// dialWorktreeHub is the production dialer: the DSN comes from the worktree's
// own secrets file, never from the process environment.
func dialWorktreeHub(opts transport.Options) hubDialer {
	return func(ctx context.Context, root string) (enrolHub, func(), error) {
		dsn, err := worktreeDSN(root)
		if err != nil {
			return nil, nil, err
		}
		connectCtx, cancel := context.WithTimeout(ctx, syncConnectBudget)
		defer cancel()
		pool, err := transport.New(connectCtx, dsn, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("connect to the hub: %w", err)
		}
		return pool, pool.Close, nil
	}
}

// newWorktreeEnrolPorts returns the production effects of the hub-board
// worktree enrolment (MTIX-112.17.16, ADR-007 section 4.2). Every hub call uses
// the worktree's own secrets file; the process environment DSN is never read.
// The set cannot remove local state: a store with local-only events is refused
// before anything moves. enroll records the enrolment (HostEnrollment.Enroll in setup).
func newWorktreeEnrolPorts(logger *slog.Logger, progress io.Writer, opts transport.Options,
	enroll func(context.Context, service.EnrollmentRequest) error, enrolled func(context.Context, string) (bool, error),
) service.WorktreePorts {
	return newWorktreeEnrolPortsWith(logger, progress, dialWorktreeHub(opts), enroll, enrolled)
}

// newWorktreeEnrolPortsWith is newWorktreeEnrolPorts over any hub dialer.
func newWorktreeEnrolPortsWith(logger *slog.Logger, progress io.Writer, dial hubDialer,
	enroll func(context.Context, service.EnrollmentRequest) error, enrolled func(context.Context, string) (bool, error),
) service.WorktreePorts {
	return service.WorktreePorts{
		Snapshot: func(ctx context.Context, dataDir string) (*service.WorktreeSnapshot, error) {
			return snapshotWorktree(ctx, logger, dataDir)
		},
		HubMissing: func(ctx context.Context, root string, ids []string) ([]string, error) {
			return hubMissingIDs(ctx, root, ids, dial)
		},
		PreflightBoard: func(ctx context.Context, root string) (*service.BoardFinding, error) {
			return preflightBoard(ctx, logger, root, dial)
		},
		VerifyBoard: func(ctx context.Context, root, asideDir string) (*service.BoardFinding, error) {
			return verifyBoardAgainstClone(ctx, logger, root, dial, asideDir)
		},
		LostLocal: func(ctx context.Context, root, asideDir string) (*service.LostLocal, error) {
			return lostLocalValues(ctx, logger, root, asideDir)
		},
		Backup: func(ctx context.Context, root, dest string) error { return backupWorktree(ctx, logger, root, dest) },
		Clone:  func(ctx context.Context, root string) error { return cloneWorktree(ctx, logger, progress, root, dial) },
		ImportCarry: func(ctx context.Context, root string, c *sqlite.InboxCarry, at time.Time) error {
			return importWorktreeCarry(ctx, logger, root, c, at)
		},
		Enroll:   enroll,
		Enrolled: enrolled,
	}
}

func worktreeDataDir(root string) string { return filepath.Join(root, ".mtix", "data") }

// worktreeDSN reads the DSN from the worktree's own secrets file only.
func worktreeDSN(root string) (string, error) {
	body, mode, err := transport.ReadSecretsFile(filepath.Join(root, ".mtix"))
	if err != nil {
		return "", fmt.Errorf("worktree secrets: %w", err)
	}
	if mode != transport.SecretsRequiredMode {
		return "", fmt.Errorf("worktree secrets file mode %#o: %w", mode, transport.ErrSecretsFileMode)
	}
	dsn := strings.TrimSpace(body)
	if dsn == "" {
		return "", transport.ErrDSNNotConfigured
	}
	return dsn, nil
}

func withWorktreeStore(logger *slog.Logger, root string, fn func(*sqlite.Store) error) error {
	return withStoreAt(logger, worktreeDataDir(root), fn)
}

func withStoreAt(logger *slog.Logger, dataDir string, fn func(*sqlite.Store) error) (retErr error) {
	st, err := sqlite.New(dataDir, logger)
	if err != nil {
		return fmt.Errorf("open worktree store: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, st.Close()) }()
	return fn(st)
}

func snapshotWorktree(ctx context.Context, logger *slog.Logger, dataDir string) (*service.WorktreeSnapshot, error) {
	snap := &service.WorktreeSnapshot{}
	err := withStoreAt(logger, dataDir, func(st *sqlite.Store) error {
		status, err := service.NewHubMarkService(st).Inspect(ctx, service.HubEvidence{})
		if err != nil {
			return fmt.Errorf("read hub mark: %w", err)
		}
		snap.HubConnected = status.Connected
		if snap.Events, err = st.LocalEvents(ctx); err != nil {
			return err
		}
		snap.Carry, err = st.ExportInboxCarry(ctx)
		return err
	})
	return snap, err
}

func backupWorktree(ctx context.Context, logger *slog.Logger, root, dest string) error {
	return withWorktreeStore(logger, root, func(st *sqlite.Store) error {
		_, err := st.Backup(ctx, dest)
		return err
	})
}

func importWorktreeCarry(ctx context.Context, logger *slog.Logger, root string, c *sqlite.InboxCarry, at time.Time) error {
	return withWorktreeStore(logger, root, func(st *sqlite.Store) error {
		_, err := st.ImportInboxCarry(ctx, c, at)
		return err
	})
}

// hubMissingIDs returns the ids the hub does not hold.
func hubMissingIDs(ctx context.Context, root string, ids []string, dial hubDialer) ([]string, error) {
	pool, closeHub, err := dial(ctx, root)
	if err != nil {
		return nil, err
	}
	defer closeHub()
	held := map[string]bool{}
	for i := 0; i < len(ids); i += hubIDLookupChunk {
		events, fetchErr := pool.FetchEventsByID(ctx, ids[i:min(i+hubIDLookupChunk, len(ids))])
		if fetchErr != nil {
			return nil, fmt.Errorf("look up events on the hub: %w", fetchErr)
		}
		for _, e := range events {
			held[e.EventID] = true
		}
	}
	var missing []string
	for _, id := range ids {
		if !held[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// cloneWorktree connects to the hub with the worktree's own secrets and seeds a
// fresh store at the worktree's data directory through the same checked clone
// path as `mtix sync clone`.
func cloneWorktree(ctx context.Context, logger *slog.Logger, progress io.Writer, root string, dial hubDialer) error {
	pool, closeHub, err := dial(ctx, root)
	if err != nil {
		return err
	}
	defer closeHub()
	return cloneWorktreeFrom(ctx, logger, progress, root, pool)
}

// cloneWorktreeFrom creates the fresh data directory and seeds the store in it
// from pool. The old store moved away with its directory; sqlite.New on a path
// that is not a directory creates a database FILE of that name, which every
// later command would then fail to open as the data directory.
func cloneWorktreeFrom(ctx context.Context, logger *slog.Logger, progress io.Writer, root string, pool cursorPuller) error {
	if err := os.MkdirAll(worktreeDataDir(root), 0o750); err != nil {
		return fmt.Errorf("create the fresh data directory: %w", err)
	}
	return withWorktreeStore(logger, root, func(st *sqlite.Store) error {
		return seedFreshStore(ctx, st, progress, pool)
	})
}

// freshStoreHoldsData reports whether st holds any event or any node: a tasks.json
// import writes nodes without journal rows, so events alone do not show it.
func freshStoreHoldsData(ctx context.Context, st *sqlite.Store) (bool, error) {
	has, err := localHasEvents(ctx, st)
	if err != nil || has {
		return has, err
	}
	var n int
	// Any node row means something other than the clone wrote to this store.
	if err := st.QueryRow(ctx, `SELECT COUNT(*) FROM nodes`).Scan(&n); err != nil {
		return false, fmt.Errorf("count nodes in the fresh store: %w", err)
	}
	return n > 0, nil
}

// seedFreshStore clones the hub into st, which must hold no event: a store that
// already holds one was written by another process after the old store moved,
// and cloning over it would mix local and hub state (the same
// guard `mtix sync clone` has). It then starts the hook scan floor at the journal tail, so the
// cloned history wakes no lane, and records the hub-connected mark.
func seedFreshStore(ctx context.Context, st *sqlite.Store, progress io.Writer, pool cursorPuller) error {
	has, err := freshStoreHoldsData(ctx, st)
	if err != nil {
		return fmt.Errorf("check the fresh store is empty: %w", err)
	}
	if has {
		return fmt.Errorf("the fresh store at the worktree's data directory already holds events or nodes written by another process; "+
			"nothing was cloned over them and the original store is restored: %w", model.ErrConflict)
	}
	if _, _, stage, cloneErr := checkThenCloneInto(ctx, st, progress, pool, transport.PullCursor{}, 1000); cloneErr != nil {
		return fmt.Errorf("clone %s: %w", stage, cloneErr)
	}
	if err := st.InitHookScanFloorAtTail(ctx); err != nil {
		return fmt.Errorf("initialize hook scan floor: %w", err)
	}
	if _, err := service.NewHubMarkService(st).HubConnected(ctx, service.HubEvidence{SecretsDSN: true}); err != nil {
		return fmt.Errorf("record hub-connected mark: %w", err)
	}
	return nil
}
