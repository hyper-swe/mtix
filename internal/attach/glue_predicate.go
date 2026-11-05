// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

// NotAttachedLine is the one line glue prints in a directory it does not act in
// (MTIX-112.19.8, ADR-007 A5).
const NotAttachedLine = "board not attached on this host"

// Reasons a glue predicate declined; doctor reports the tracked-store ones as
// glue.tracked-store.
const (
	ReasonAttached         = "attached"
	ReasonNoCheckout       = "no-checkout"
	ReasonStateUnavailable = "state-unavailable"
	ReasonUnlisted         = "unlisted"
	ReasonIdentityMismatch = "identity-mismatch"
	ReasonTrackedStore     = "tracked-store"
	ReasonIndexUnsupported = "index-unsupported"
)

// boardStoreRel is the store file relative to a checkout root.
const boardStoreRel = ".mtix/data/mtix.db"

// PredicateDeps are the host-local facts the predicate reads. Nothing here
// writes: the registry read creates no state, and the store identity is read
// through a read-only open.
type PredicateDeps struct {
	// Boards returns the operator-local listed and enrolled boards.
	Boards func(ctx context.Context) (oplocal.DispatchBoards, error)
	// StoreInstanceID reads store_instance_id from the store file read-only.
	StoreInstanceID func(ctx context.Context, dbPath string) (string, error)
}

// GlueDecision is whether glue may act in a directory.
type GlueDecision struct {
	Act bool
	// Reason is one of the Reason constants.
	Reason string
	// Line is NotAttachedLine when Act is false.
	Line string
	// BoardRoot is the listed checkout root the decision matched.
	BoardRoot string
	// Worktree is true when the directory is a linked worktree of BoardRoot.
	Worktree bool
	// BoardKey is the key of the matched listed (or enrolled) record.
	BoardKey string
}

func decline(reason string) GlueDecision {
	return GlueDecision{Reason: reason, Line: NotAttachedLine}
}

// EvaluateGlue decides whether harness glue run in dir may act. It acts only
// when dir, or the main checkout of a verified linked worktree, is listed or
// enrolled in operator-local state and the store path and store_instance_id
// there match the record. A listing with no instance id (a legacy store) must
// additionally pass the committed-store test on the git index. Every failure
// declines (fail closed) (MTIX-112.19.8, ADR-007 A5).
func EvaluateGlue(ctx context.Context, deps PredicateDeps, dir string) GlueDecision {
	if deps.Boards == nil || deps.StoreInstanceID == nil {
		return decline(ReasonStateUnavailable)
	}
	co, err := DetectCheckout(dir)
	if err != nil || co.Kind == CheckoutNone {
		return decline(ReasonNoCheckout)
	}
	boards, err := deps.Boards(ctx)
	if err != nil {
		return decline(ReasonStateUnavailable)
	}
	root := co.Root
	if co.Kind == CheckoutWorktree {
		root = co.CommonRoot
	}
	ids, key, found := recordedInstances(boards, root)
	if !found {
		return decline(ReasonUnlisted)
	}
	actual, err := readStoreID(ctx, deps, root)
	if err != nil {
		return decline(ReasonIdentityMismatch)
	}
	legacy := false
	for _, id := range ids {
		if id != actual {
			return decline(ReasonIdentityMismatch)
		}
		legacy = legacy || id == ""
	}
	if legacy {
		if reason := committedStoreReason(co); reason != "" {
			return decline(reason)
		}
	}
	return GlueDecision{Act: true, Reason: ReasonAttached, BoardRoot: root, Worktree: co.Kind == CheckoutWorktree, BoardKey: key}
}

// recordedInstances returns the instance ids of every listed or enrolled
// record for root, the board key of the first (listed before enrolled), and
// whether any record exists.
func recordedInstances(b oplocal.DispatchBoards, root string) (ids []string, key string, found bool) {
	for _, row := range b.Listed {
		if row.Path == root {
			ids = append(ids, row.StoreInstanceID)
			if key == "" {
				key = row.Key
			}
		}
	}
	for _, row := range b.Enrolled {
		if row.Path == root {
			ids = append(ids, row.StoreInstanceID)
			if key == "" {
				key = row.BoardID
			}
		}
	}
	return ids, key, len(ids) > 0
}

// readStoreID reads the instance id of root's store; a missing store file is an error.
func readStoreID(ctx context.Context, deps PredicateDeps, root string) (string, error) {
	db := filepath.Join(root, filepath.FromSlash(boardStoreRel))
	if info, err := os.Lstat(db); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("store file is absent or not a regular file")
	}
	return deps.StoreInstanceID(ctx, db)
}

// committedStoreReason runs the bounded index reader over the invoking
// checkout's index and names why glue must not act, or returns "".
func committedStoreReason(co Checkout) string {
	tracked, err := StoreTrackedInIndex(co.GitDir, boardStoreRel)
	switch {
	case err != nil: // ErrIndexUnsupported and any read failure alike fail closed
		return ReasonIndexUnsupported
	case tracked:
		return ReasonTrackedStore
	}
	return ""
}
