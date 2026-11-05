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
	// Worktree is true when the directory is a linked worktree attached through
	// its main checkout BoardRoot (its own root is not listed).
	Worktree bool
	// BoardKey is the key of the matched listed (or enrolled) record.
	BoardKey string
	// Cause refines a tracked-store or index-unsupported decline (a Cause or
	// Tracked constant) so doctor can print one remedy per cause.
	Cause string
}

func decline(reason string) GlueDecision {
	return GlueDecision{Reason: reason, Line: NotAttachedLine}
}

// glueCandidate is one root the directory could be attached through, with the
// git dir whose index answers the committed-store question ("" for a board
// that is not a git checkout).
type glueCandidate struct{ root, gitDir string }

// EvaluateGlue decides whether harness glue run in dir may act. dir is resolved
// to its physical path, as listed paths are. It acts only when the checkout
// root of dir, or for a verified linked worktree its own root first and then
// the main checkout, or for a directory with no git checkout the nearest
// project root holding .mtix, is listed or enrolled in operator-local state and
// the store path and store_instance_id there match the record. A listing with
// no instance id (a legacy store) must additionally pass the committed-store
// test on the git index. Every failure declines (fail closed) (MTIX-112.19.8,
// ADR-007 A5).
func EvaluateGlue(ctx context.Context, deps PredicateDeps, dir string) GlueDecision {
	if deps.Boards == nil || deps.StoreInstanceID == nil {
		return decline(ReasonStateUnavailable)
	}
	co, err := DetectCheckout(dir)
	if err != nil || co.Kind == CheckoutInvalid {
		return decline(ReasonNoCheckout)
	}
	candidates := glueCandidates(co, dir)
	if len(candidates) == 0 {
		return decline(ReasonNoCheckout)
	}
	boards, err := deps.Boards(ctx)
	if err != nil {
		return decline(ReasonStateUnavailable)
	}
	for _, c := range candidates {
		if ids, key, found := recordedInstances(boards, c.root); found {
			return judgeCandidate(ctx, deps, c, ids, key, co.Kind == CheckoutWorktree && c.root != co.Root)
		}
	}
	return decline(ReasonUnlisted)
}

// glueCandidates lists the roots to try, most specific first.
func glueCandidates(co Checkout, dir string) []glueCandidate {
	switch co.Kind {
	case CheckoutPlain:
		return []glueCandidate{{co.Root, co.GitDir}}
	case CheckoutWorktree:
		return []glueCandidate{{co.Root, co.GitDir}, {co.CommonRoot, filepath.Join(co.CommonRoot, ".git")}}
	}
	if root := projectRootWithoutGit(dir); root != "" {
		return []glueCandidate{{root, ""}}
	}
	return nil
}

// projectRootWithoutGit returns the nearest physical ancestor of dir holding a
// .mtix directory, or "".
func projectRootWithoutGit(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	cur, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ""
	}
	for depth := 0; depth < maxWalkDepth; depth++ {
		if info, statErr := os.Lstat(filepath.Join(cur, ".mtix")); statErr == nil && info.IsDir() {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return ""
}

// judgeCandidate compares the records for c.root with its store and, for a
// legacy listing, with the git index.
func judgeCandidate(ctx context.Context, deps PredicateDeps, c glueCandidate, ids []string, key string, viaCommon bool) GlueDecision {
	actual, err := readStoreID(ctx, deps, c.root)
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
	if legacy && c.gitDir != "" {
		if reason, cause := committedStoreReason(c.gitDir); reason != "" {
			d := decline(reason)
			d.Cause = cause
			return d
		}
	}
	return GlueDecision{Act: true, Reason: ReasonAttached, BoardRoot: c.root, Worktree: viaCommon, BoardKey: key}
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

// readStoreID reads the instance id of root's store. Every component of the
// store path must be a real directory or regular file: a committed symlink over
// .mtix or .mtix/data must not lend a store from elsewhere.
func readStoreID(ctx context.Context, deps PredicateDeps, root string) (string, error) {
	for _, rel := range []string{".mtix", ".mtix/data"} {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || !info.IsDir() {
			return "", errors.New("store directory is absent or not a real directory")
		}
	}
	db := filepath.Join(root, filepath.FromSlash(boardStoreRel))
	if info, err := os.Lstat(db); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("store file is absent or not a regular file")
	}
	return deps.StoreInstanceID(ctx, db)
}

// committedStoreReason runs the bounded index reader over gitDir's index and
// names why glue must not act (with the cause doctor keys its remedy on), or
// returns "".
func committedStoreReason(gitDir string) (reason, cause string) {
	kind, err := ScanStoreInIndex(gitDir, boardStoreRel)
	var ie *IndexError
	switch {
	case errors.As(err, &ie):
		return ReasonIndexUnsupported, ie.Cause
	case err != nil:
		return ReasonIndexUnsupported, CauseMalformed
	case kind != TrackedNone:
		return ReasonTrackedStore, kind
	}
	return "", ""
}
