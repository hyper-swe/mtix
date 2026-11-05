// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"context"
	"errors"
	"os"
	"path"
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
	// StoreRoot is the physical project root of the store the verb would open:
	// the parent of the nearest .mtix above the physical working directory. It
	// is empty only when no store was found. Everything after the gate uses
	// this store and none re-discovers one.
	StoreRoot string
	// BoardRoot is StoreRoot when the decision matched a listed or enrolled
	// record, and empty otherwise.
	BoardRoot string
	// BoardKey is the key of the matched listed (or enrolled) record.
	BoardKey string
	// CheckoutRoot is the root of the git checkout holding StoreRoot ("" when
	// the store is not in a checkout); doctor builds its git commands from it.
	CheckoutRoot string
	// Cause refines a tracked-store or index-unsupported decline (a Cause or
	// Tracked constant) so doctor can print one remedy per cause.
	Cause string
}

func decline(reason string) GlueDecision {
	return GlueDecision{Reason: reason, Line: NotAttachedLine}
}

// declined turns d into a decline for reason, keeping the facts doctor needs.
func (d GlueDecision) declined(reason, cause string) GlueDecision {
	d.Act, d.Reason, d.Cause, d.Line = false, reason, cause, NotAttachedLine
	d.BoardRoot, d.BoardKey = "", ""
	return d
}

// EvaluateGlue decides whether harness glue run in dir may act. It resolves
// once: dir becomes its physical path, and the nearest .mtix at or above it
// names the store the verb would open. That store, and only that store, is
// judged: its project root must be listed or enrolled in operator-local state,
// and the store path and store_instance_id there must match the record. A
// store nested under a listed checkout, or committed on a worktree branch, is
// judged as itself, so it is declined unless it is listed. A listing with no
// instance id (a legacy store) must additionally pass the committed-store test
// on the index of the checkout holding the store. Every failure declines (fail
// closed) (MTIX-112.19.8, ADR-007 A5).
func EvaluateGlue(ctx context.Context, deps PredicateDeps, dir string) GlueDecision {
	if deps.Boards == nil || deps.StoreInstanceID == nil {
		return decline(ReasonStateUnavailable)
	}
	root := nearestStoreRoot(dir)
	if root == "" {
		return decline(ReasonNoCheckout)
	}
	base := GlueDecision{StoreRoot: root, Line: NotAttachedLine}
	co, err := DetectCheckout(root)
	if err != nil || co.Kind == CheckoutInvalid {
		return base.declined(ReasonNoCheckout, "")
	}
	base.CheckoutRoot = co.Root
	boards, err := deps.Boards(ctx)
	if err != nil {
		return base.declined(ReasonStateUnavailable, "")
	}
	ids, key, found := recordedInstances(boards, root)
	if !found {
		return base.declined(ReasonUnlisted, "")
	}
	return judgeStore(ctx, deps, base, co, ids, key)
}

// nearestStoreRoot returns the parent of the nearest .mtix at or above the
// physical path of dir, walking the way the verbs find their board (a .mtix
// that is a directory, or a symlink to one, counts; the store checks reject a
// symlink later), or "".
func nearestStoreRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	cur, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ""
	}
	for depth := 0; depth < maxWalkDepth; depth++ {
		if info, statErr := os.Stat(filepath.Join(cur, ".mtix")); statErr == nil && info.IsDir() {
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

// judgeStore compares the records for the store root with the store itself and,
// for a legacy listing, with the index of the checkout holding it.
func judgeStore(ctx context.Context, deps PredicateDeps, base GlueDecision, co Checkout, ids []string, key string) GlueDecision {
	actual, err := readStoreID(ctx, deps, base.StoreRoot)
	if err != nil {
		return base.declined(ReasonIdentityMismatch, "")
	}
	legacy := false
	for _, id := range ids {
		if id != actual {
			return base.declined(ReasonIdentityMismatch, "")
		}
		legacy = legacy || id == ""
	}
	if legacy && co.Kind != CheckoutNone {
		rel, relErr := filepath.Rel(co.Root, base.StoreRoot)
		if relErr != nil {
			return base.declined(ReasonIndexUnsupported, CauseMalformed)
		}
		storeRel := path.Join(filepath.ToSlash(rel), boardStoreRel)
		if reason, cause := committedStoreReason(co.GitDir, storeRel); reason != "" {
			return base.declined(reason, cause)
		}
	}
	base.Act, base.Reason, base.Line = true, ReasonAttached, ""
	base.BoardRoot, base.BoardKey = base.StoreRoot, key
	return base
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

// committedStoreReason runs the bounded index reader over gitDir's index for the
// store at storeRel (a slash path from the checkout root) and
// names why glue must not act (with the cause doctor keys its remedy on), or
// returns "".
func committedStoreReason(gitDir, storeRel string) (reason, cause string) {
	kind, err := ScanStoreInIndex(gitDir, storeRel)
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
