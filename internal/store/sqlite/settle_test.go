// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// newUID returns a fresh UUIDv7 usable as a node uid (create-event id).
func newUID(t *testing.T) string {
	t.Helper()
	uid, err := clock.NewEventID()
	require.NoError(t, err)
	return uid
}

// provisionalChild builds and stores a provisional child under parentID with a
// uid-bearing display path (ADR-003 §4) and returns the stored node.
func provisionalChild(
	t *testing.T, s *sqlite.Store, parentID, project, title string, depth int,
) *model.Node {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// UUIDv7 is timestamp-prefixed, so the lossy short uid segment can repeat
	// for uids minted in the same millisecond (the cosmetic collision noted in
	// ADR-003 §13). Retry with fresh uids until the provisional id is unique so
	// the test exercises settlement, not that incidental clash.
	for {
		uid := newUID(t)
		provID, err := model.BuildProvisionalID(parentID, uid)
		require.NoError(t, err)

		node := makeChildNode(provID, parentID, project, title, depth, 0, now)
		node.UID = uid
		err = s.CreateNode(ctx, node)
		if errors.Is(err, model.ErrAlreadyExists) {
			continue
		}
		require.NoError(t, err)
		return node
	}
}

// recordingSettler is a programmable Settler test double: it records every
// ConfirmClaim call and returns a scripted outcome (ADR-003 §4 claim protocol).
type recordingSettler struct {
	mu       sync.Mutex
	calls    []sqlite.ClaimRequest
	outcomes []sqlite.SettleOutcome // consumed in order; last value repeats
	errs     []error
	idx      int
}

func (r *recordingSettler) ConfirmClaim(
	_ context.Context, req sqlite.ClaimRequest,
) (sqlite.SettleOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	i := r.idx
	if i >= len(r.outcomes) {
		i = len(r.outcomes) - 1
	}
	var err error
	if r.idx < len(r.errs) {
		err = r.errs[r.idx]
	}
	r.idx++
	if i < 0 {
		return sqlite.SettleConfirmed, err
	}
	return r.outcomes[i], err
}

func (r *recordingSettler) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordingSettler) lastDisplayPaths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	for i, c := range r.calls {
		out[i] = c.DisplayPath
	}
	return out
}

// --- ClaimNextSeq (local eager claim) -------------------------------------

// TestClaimNextSeq_RootNamespace claims sequential numbers under the project
// root namespace (ADR-003 §4 eager local claim).
func TestClaimNextSeq_RootNamespace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.ClaimNextSeq(ctx, "PROJ", "")
	require.NoError(t, err)
	b, err := s.ClaimNextSeq(ctx, "PROJ", "")
	require.NoError(t, err)

	assert.Equal(t, 1, a)
	assert.Equal(t, 2, b, "claims under the same namespace must be distinct & monotonic")
}

// TestClaimNextSeq_ChildNamespace claims under a parent's namespace,
// independent of the root namespace.
func TestClaimNextSeq_ChildNamespace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	c1, err := s.ClaimNextSeq(ctx, "PROJ", "PROJ-1")
	require.NoError(t, err)
	c2, err := s.ClaimNextSeq(ctx, "PROJ", "PROJ-1")
	require.NoError(t, err)

	assert.Equal(t, 1, c1)
	assert.Equal(t, 2, c2)
}

// TestClaimNextSeq_BurstDistinct is the corner case: a burst of sibling claims
// must each get a distinct number (ADR-003 §4 / §6 collision-free settling).
func TestClaimNextSeq_BurstDistinct(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const n = 50
	var mu sync.Mutex
	seen := map[int]bool{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seq, err := s.ClaimNextSeq(ctx, "PROJ", "PROJ-9")
			assert.NoError(t, err)
			mu.Lock()
			seen[seq] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	assert.Len(t, seen, n, "every concurrent sibling claim must be distinct")
}

// --- SettleNode (claim-confirm + clean renumber) --------------------------

// TestSettleNode_Online_SettlesToCleanNumber is the happy path: a provisional
// node settles to a clean numeric display path and is confirmed (ADR-003 §4).
func TestSettleNode_Online_SettlesToCleanNumber(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "root", now)))
	child := provisionalChild(t, s, "PROJ-1", "PROJ", "child", 1)
	require.True(t, model.IsProvisional(child.ID))

	settler := &recordingSettler{outcomes: []sqlite.SettleOutcome{sqlite.SettleConfirmed}}
	settled, err := s.SettleNode(ctx, settler, child.UID)
	require.NoError(t, err)

	assert.Equal(t, "PROJ-1.1", settled)
	assert.False(t, model.IsProvisional(settled))

	got, err := s.ResolveDisplayPathByUID(ctx, child.UID)
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1.1", got, "uid must resolve to the new clean path")
}

// TestSettleNode_ConfirmBeforeRenumberCommitsClean asserts the invariant: the
// settler ConfirmClaim is called for the clean candidate path (claim-confirm)
// before SettleNode reports the node settled (ADR-003 §4 invariant).
func TestSettleNode_ConfirmsCleanCandidate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "root", now)))
	child := provisionalChild(t, s, "PROJ-1", "PROJ", "child", 1)

	settler := &recordingSettler{outcomes: []sqlite.SettleOutcome{sqlite.SettleConfirmed}}
	_, err := s.SettleNode(ctx, settler, child.UID)
	require.NoError(t, err)

	require.Equal(t, 1, settler.callCount())
	assert.Equal(t, []string{"PROJ-1.1"}, settler.lastDisplayPaths(),
		"claim-confirm must target the clean (numeric) candidate, never the provisional path")
}

// TestSettleNode_RetryOnTaken claims the NEXT free seq when the hub reports the
// number already taken (ADR-003 §4 retry-on-taken / §6 first-writer-wins).
func TestSettleNode_RetryOnTaken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "root", now)))
	child := provisionalChild(t, s, "PROJ-1", "PROJ", "child", 1)

	// First candidate (PROJ-1.1) is taken on the hub; second (PROJ-1.2) wins.
	settler := &recordingSettler{outcomes: []sqlite.SettleOutcome{
		sqlite.SettleRenumberRequired,
		sqlite.SettleConfirmed,
	}}
	settled, err := s.SettleNode(ctx, settler, child.UID)
	require.NoError(t, err)

	assert.Equal(t, "PROJ-1.2", settled)
	assert.Equal(t, []string{"PROJ-1.1", "PROJ-1.2"}, settler.lastDisplayPaths())
}

// TestSettleNode_Offline_StaysProvisional is the offline fallback: an
// unreachable hub leaves the node provisional for a later retry (ADR-003 §4).
func TestSettleNode_Offline_StaysProvisional(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "root", now)))
	child := provisionalChild(t, s, "PROJ-1", "PROJ", "child", 1)

	settler := &recordingSettler{outcomes: []sqlite.SettleOutcome{sqlite.SettleUnreachable}}
	settled, err := s.SettleNode(ctx, settler, child.UID)
	require.ErrorIs(t, err, sqlite.ErrHubUnreachable)
	assert.Empty(t, settled)

	// Node remains provisional and resolvable for a later retry.
	got, err := s.ResolveDisplayPathByUID(ctx, child.UID)
	require.NoError(t, err)
	assert.True(t, model.IsProvisional(got), "offline node must stay provisional")
	assert.Equal(t, child.ID, got)
}

// TestSettleNode_AlreadySettled is the idempotent no-op: settling an
// already-clean node confirms nothing and changes nothing (ADR-003 §4).
func TestSettleNode_AlreadySettled(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "root", now)))
	clean := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "clean", 1, 1, now)
	clean.UID = newUID(t)
	require.NoError(t, s.CreateNode(ctx, clean))

	settler := &recordingSettler{outcomes: []sqlite.SettleOutcome{sqlite.SettleConfirmed}}
	settled, err := s.SettleNode(ctx, settler, clean.UID)
	require.NoError(t, err)

	assert.Equal(t, "PROJ-1.1", settled)
	assert.Equal(t, 0, settler.callCount(), "an already-settled node needs no claim-confirm")
}

// TestSettleNode_ParentUnsettled refuses to settle a node whose ancestor is
// still provisional — a clean child cannot exist under an unsettled ancestor
// (ADR-003 §4, §5).
func TestSettleNode_ParentUnsettled(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "root", now)))
	parent := provisionalChild(t, s, "PROJ-1", "PROJ", "prov-parent", 1)
	grandchild := provisionalChild(t, s, parent.ID, "PROJ", "grandchild", 2)

	settler := &recordingSettler{outcomes: []sqlite.SettleOutcome{sqlite.SettleConfirmed}}
	_, err := s.SettleNode(ctx, settler, grandchild.UID)
	require.ErrorIs(t, err, sqlite.ErrAncestorUnsettled)
	assert.Equal(t, 0, settler.callCount())
}

// TestSettleNode_UnknownUID returns ErrNotFound for a uid with no live node.
func TestSettleNode_UnknownUID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	settler := &recordingSettler{outcomes: []sqlite.SettleOutcome{sqlite.SettleConfirmed}}
	_, err := s.SettleNode(ctx, settler, newUID(t))
	require.ErrorIs(t, err, model.ErrNotFound)
}

// TestSettleNode_SettlerError surfaces a transport error without changing the
// node (ADR-003 §9: a broken hub can at worst force a retry, never lose a node).
func TestSettleNode_SettlerError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "root", now)))
	child := provisionalChild(t, s, "PROJ-1", "PROJ", "child", 1)

	boom := errors.New("hub exploded")
	settler := &recordingSettler{
		outcomes: []sqlite.SettleOutcome{sqlite.SettleConfirmed},
		errs:     []error{boom},
	}
	_, err := s.SettleNode(ctx, settler, child.UID)
	require.ErrorIs(t, err, boom)

	got, err := s.ResolveDisplayPathByUID(ctx, child.UID)
	require.NoError(t, err)
	assert.True(t, model.IsProvisional(got), "node must be unchanged on settler error")
}

// invariantSettler asserts the ADR-003 §4 happens-before invariant: at the
// moment ConfirmClaim runs (the claim-confirm), the node it is confirming MUST
// still be provisional in the store — it must not yet be settled. This proves the
// claim-confirm happens-before the node is observable as settled / pushable as
// settled.
type invariantSettler struct {
	t   *testing.T
	s   *sqlite.Store
	uid string
}

func (iv *invariantSettler) ConfirmClaim(
	ctx context.Context, req sqlite.ClaimRequest,
) (sqlite.SettleOutcome, error) {
	id, err := iv.s.ResolveDisplayPathByUID(ctx, iv.uid)
	require.NoError(iv.t, err)
	assert.True(iv.t, model.IsProvisional(id),
		"node must still be provisional at claim-confirm time (ADR-003 §4 invariant)")
	return sqlite.SettleConfirmed, nil
}

// TestSettleNode_ConfirmHappensBeforeSettled enforces the ADR-003 §4 invariant:
// the claim-confirm for a node happens-before the node is settled. A node never
// transitions to a clean (settled, pushable) id without a prior confirm.
func TestSettleNode_ConfirmHappensBeforeSettled(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "root", now)))
	child := provisionalChild(t, s, "PROJ-1", "PROJ", "child", 1)

	iv := &invariantSettler{t: t, s: s, uid: child.UID}
	settled, err := s.SettleNode(ctx, iv, child.UID)
	require.NoError(t, err)
	assert.False(t, model.IsProvisional(settled),
		"node is settled only AFTER its claim-confirm returns")
}

// --- PendingSettlements ----------------------------------------------------

// TestPendingSettlements_OnlyProvisionalReady lists provisional nodes whose
// ancestors are all settled, shallow-first, and excludes settled nodes and
// nodes still blocked by an unsettled ancestor (ADR-003 §4 next-sync retry).
func TestPendingSettlements_OnlyProvisionalReady(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "root", now)))
	clean := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "clean", 1, 1, now)
	clean.UID = newUID(t)
	require.NoError(t, s.CreateNode(ctx, clean))

	readyA := provisionalChild(t, s, "PROJ-1", "PROJ", "readyA", 1)
	provParent := provisionalChild(t, s, "PROJ-1", "PROJ", "provParent", 1)
	blocked := provisionalChild(t, s, provParent.ID, "PROJ", "blocked", 2)

	pending, err := s.PendingSettlements(ctx)
	require.NoError(t, err)

	assert.Contains(t, pending, readyA.UID)
	assert.Contains(t, pending, provParent.UID)
	assert.NotContains(t, pending, clean.UID, "settled nodes are not pending")
	assert.NotContains(t, pending, blocked.UID,
		"a node under an unsettled ancestor is not yet ready to settle")
}

// TestPendingSettlements_EmptyWhenAllSettled returns nothing when every node is
// already clean (the steady-state online case).
func TestPendingSettlements_EmptyWhenAllSettled(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "root", now)))
	clean := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "clean", 1, 1, now)
	clean.UID = newUID(t)
	require.NoError(t, s.CreateNode(ctx, clean))

	pending, err := s.PendingSettlements(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending)
}
