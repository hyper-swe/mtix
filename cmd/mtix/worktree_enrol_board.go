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

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func boardFilePath(root string) string { return filepath.Join(root, ".mtix", "tasks.json") }

// boardFileFinding proves the first rule of the enrolment: tasks.json is what
// the local store exports, compared by content and never by file time. An
// absent file holds nothing to lose. A file that cannot be read or parsed is an
// error, never a pass (MTIX-112.17.16.6).
func boardFileFinding(ctx context.Context, tasksPath string, local *sqlite.Store) (*service.BoardFinding, error) {
	f, err := os.Open(tasksPath) //nolint:gosec // G304: the worktree's own board file
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", tasksPath, err)
	}
	file, decodeErr := sqlite.DecodeExportData(f)
	if closeErr := f.Close(); closeErr != nil || decodeErr != nil {
		return nil, errors.Join(decodeErr, closeErr)
	}
	exported, err := local.Export(ctx, "", "")
	if err != nil {
		return nil, fmt.Errorf("export the local store: %w", err)
	}
	ids, err := service.BoardFileDifferences(file, exported)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	return &service.BoardFinding{Reason: service.BoardFileDiffers, IDs: ids}, nil
}

// boardNodeFinding proves the second and third rules: every node of the local
// store is on the hub by uid, in a state the local journal explains.
func boardNodeFinding(ctx context.Context, local, hub *sqlite.Store) (*service.BoardFinding, error) {
	localStates, err := local.BoardNodeStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the local nodes: %w", err)
	}
	hubStates, err := hub.BoardNodeStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the hub copy's nodes: %w", err)
	}
	ids := service.BoardNodesHubLacks(localStates, hubStates)
	if len(ids) == 0 {
		return nil, nil
	}
	return &service.BoardFinding{Reason: service.BoardHubLacks, IDs: ids}, nil
}

// verifyBoardAgainstClone repeats the proof once the swap happened: the old
// store (asideDir) against the store now in the worktree's data directory,
// which is the hub as it was at the moment of the clone.
func verifyBoardAgainstClone(ctx context.Context, logger *slog.Logger, root, asideDir string) (finding *service.BoardFinding, err error) {
	err = withStoreAt(logger, asideDir, func(local *sqlite.Store) error {
		return withWorktreeStore(logger, root, func(clone *sqlite.Store) (e error) {
			finding, e = boardFinding(ctx, root, local, clone)
			return e
		})
	})
	return finding, err
}

func boardFinding(ctx context.Context, root string, local, hub *sqlite.Store) (*service.BoardFinding, error) {
	if f, err := boardFileFinding(ctx, boardFilePath(root), local); err != nil || f != nil {
		return f, err
	}
	return boardNodeFinding(ctx, local, hub)
}

// preflightBoard proves the local board is all on the hub before the real store
// moves. The file rule is checked first, without touching the hub; only then is
// the hub cloned into a scratch store under the enrolment's own directory,
// which is deleted file by file when done. Each enrolment clones the hub twice:
// this scratch copy, then the real one.
func preflightBoard(ctx context.Context, logger *slog.Logger, root string, dial hubDialer) (finding *service.BoardFinding, err error) {
	err = withWorktreeStore(logger, root, func(local *sqlite.Store) error {
		var e error
		if finding, e = boardFileFinding(ctx, boardFilePath(root), local); e != nil || finding != nil {
			return e
		}
		pool, closeHub, e := dial(ctx, root)
		if e != nil {
			return e
		}
		defer closeHub()
		return withScratchHub(ctx, logger, root, pool, func(hub *sqlite.Store) (err error) {
			finding, err = boardNodeFinding(ctx, local, hub)
			return err
		})
	})
	return finding, err
}

// withScratchHub clones pool into a scratch store and runs fn on it, then
// removes the scratch directory.
func withScratchHub(ctx context.Context, logger *slog.Logger, root string, pool cursorPuller, fn func(*sqlite.Store) error) (retErr error) {
	if err := os.MkdirAll(filepath.Join(root, ".mtix", "enroll.d"), 0o700); err != nil {
		return fmt.Errorf("create the enrolment directory: %w", err)
	}
	scratch, err := os.MkdirTemp(filepath.Join(root, ".mtix", "enroll.d"), "preflight-")
	if err != nil {
		return fmt.Errorf("create the scratch store directory: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, removeScratchTree(scratch)) }()
	return withStoreAt(logger, scratch, func(st *sqlite.Store) error {
		if seedErr := seedFreshStore(ctx, st, io.Discard, pool); seedErr != nil {
			return fmt.Errorf("copy the hub into the scratch store: %w", seedErr)
		}
		return fn(st)
	})
}

// removeScratchTree deletes the scratch directory the preflight created: each
// directory's files first, then the emptied directory itself, bottom-up. Links
// are removed as links, never followed.
func removeScratchTree(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list the scratch directory: %w", err)
	}
	for _, e := range entries {
		child := filepath.Join(dir, e.Name())
		if e.Type().IsDir() {
			err = errors.Join(err, removeScratchTree(child))
			continue
		}
		err = errors.Join(err, os.Remove(child))
	}
	return errors.Join(err, os.Remove(dir))
}
