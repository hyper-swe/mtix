// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// hubIDLookupChunk bounds the ids one hub lookup carries.
const hubIDLookupChunk = 500

// newWorktreeEnrolPorts returns the production effects of the hub-board
// worktree enrolment (MTIX-112.17.16, ADR-007 section 4.2). Every hub call uses
// the worktree's own secrets file; the process environment DSN is never read.
// The set cannot remove local state: a store with local-only events is refused
// before anything moves. enroll records the enrolment (HostEnrollment.Enroll in setup).
func newWorktreeEnrolPorts(logger *slog.Logger, progress io.Writer, opts transport.Options, enroll func(context.Context, service.EnrollmentRequest) error) service.WorktreePorts {
	return service.WorktreePorts{
		Snapshot: func(ctx context.Context, root string) (*service.WorktreeSnapshot, error) {
			return snapshotWorktree(ctx, logger, root)
		},
		HubMissing: func(ctx context.Context, root string, ids []string) ([]string, error) {
			return hubMissingIDs(ctx, root, ids, opts)
		},
		Backup: func(ctx context.Context, root, dest string) error { return backupWorktree(ctx, logger, root, dest) },
		Clone:  func(ctx context.Context, root string) error { return cloneWorktree(ctx, logger, progress, root, opts) },
		ImportCarry: func(ctx context.Context, root string, c *sqlite.InboxCarry, at time.Time) error {
			return importWorktreeCarry(ctx, logger, root, c, at)
		},
		Enroll: enroll,
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

func withWorktreeStore(logger *slog.Logger, root string, fn func(*sqlite.Store) error) (retErr error) {
	st, err := sqlite.New(worktreeDataDir(root), logger)
	if err != nil {
		return fmt.Errorf("open worktree store: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, st.Close()) }()
	return fn(st)
}

func snapshotWorktree(ctx context.Context, logger *slog.Logger, root string) (*service.WorktreeSnapshot, error) {
	snap := &service.WorktreeSnapshot{}
	err := withWorktreeStore(logger, root, func(st *sqlite.Store) error {
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
func hubMissingIDs(ctx context.Context, root string, ids []string, opts transport.Options) ([]string, error) {
	dsn, err := worktreeDSN(root)
	if err != nil {
		return nil, err
	}
	connectCtx, cancel := context.WithTimeout(ctx, syncConnectBudget)
	defer cancel()
	pool, err := transport.New(connectCtx, dsn, opts)
	if err != nil {
		return nil, fmt.Errorf("connect to the hub: %w", err)
	}
	defer pool.Close()
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

// cloneWorktree seeds a fresh store at the worktree's data directory through
// the same checked clone path as `mtix sync clone`, and records the
// hub-connected mark with the worktree's secrets as the evidence.
func cloneWorktree(ctx context.Context, logger *slog.Logger, progress io.Writer, root string, opts transport.Options) error {
	dsn, err := worktreeDSN(root)
	if err != nil {
		return err
	}
	return withWorktreeStore(logger, root, func(st *sqlite.Store) error {
		connectCtx, cancel := context.WithTimeout(ctx, syncConnectBudget)
		defer cancel()
		pool, err := transport.New(connectCtx, dsn, opts)
		if err != nil {
			return fmt.Errorf("connect to the hub: %w", err)
		}
		defer pool.Close()
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
	})
}
