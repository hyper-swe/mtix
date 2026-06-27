// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Regression test for MTIX-28: concurrent create-under-same-parent
// collision (ADR-003 §6, §12 scenario 2 + §12 scenario 9 retry-on-taken).
//
// This WAS a characterization test pinning the split-brain defect. MTIX-30.7
// fixes it: the hub registry serializes concurrent creates (first-writer-
// wins) and the client re-claims the next free number on a renumber-required
// outcome, so two concurrent creates under one parent settle to DISTINCT
// numbers with BOTH nodes preserved. The assertions below are the flipped,
// DESIRED post-fix invariant: no split-brain, no silent loss, all replicas
// (including a fresh clone) converge on identical content.
//
// Runs against any real Postgres hub. Skips when MTIX_PG_TEST_DSN is
// unset (same gate as the rest of the sync e2e suite). To run against
// Neon verbatim:
//
//	MTIX_PG_TEST_DSN='postgres://USER:PASS@ep-xxxx.neon.tech/db?sslmode=require' \
//	  go test ./e2e/ -run TestE2E_Collision -count=1 -v
package e2e

import (
	"context"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allTitles returns every node title in a CLI's local store.
func allTitles(t *testing.T, c *fakeCLI) []string {
	t.Helper()
	ids := c.listNodeIDs(t)
	titles := make([]string, 0, len(ids))
	for _, id := range ids {
		titles = append(titles, c.titleOf(t, id))
	}
	return titles
}

// titleByContent finds, across a CLI's nodes, the id whose title equals
// want. Fails the test if no node carries that title — used to assert a
// specific ticket survived (and to learn WHICH number it settled into,
// which is order-dependent and therefore not hard-coded).
func titleByContent(t *testing.T, c *fakeCLI, want string) string {
	t.Helper()
	for _, id := range c.listNodeIDs(t) {
		if c.titleOf(t, id) == want {
			return id
		}
	}
	t.Fatalf("%s has no node titled %q; titles=%v", c.name, want, allTitles(t, c))
	return ""
}

// assertConverged asserts every CLI in clis has byte-identical (id,title)
// content — the no-split-brain invariant (ADR-003 §6, §11). It checks the
// id set is equal and that each id maps to the same title on every replica.
func assertConverged(t *testing.T, clis ...*fakeCLI) {
	t.Helper()
	require.NotEmpty(t, clis)
	wantIDs := clis[0].listNodeIDs(t)
	for _, c := range clis[1:] {
		require.Equal(t, wantIDs, c.listNodeIDs(t),
			"%s must converge on the same id SET as %s", c.name, clis[0].name)
	}
	for _, id := range wantIDs {
		want := clis[0].titleOf(t, id)
		for _, c := range clis[1:] {
			require.Equal(t, want, c.titleOf(t, id),
				"replicas disagree on %s: %s=%q %s=%q", id, clis[0].name, want, c.name, c.titleOf(t, id))
		}
	}
}

// TestE2E_Collision_ConcurrentCreateUnderSameParent is the flipped MTIX-28
// regression: two users who each file a DIFFERENT ticket under the same
// parent before syncing must BOTH survive at DISTINCT numbers, and every
// replica (including a fresh clone) must converge on identical content.
// This is the exact original MTIX-28 scenario, now asserting the post-fix
// invariant (ADR-003 §6 first-writer-wins + retry-on-taken, §12 sc.2/9).
func TestE2E_Collision_ConcurrentCreateUnderSameParent(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	user1 := newFakeCLI(t, "user1", "1111111111111111")
	user2 := newFakeCLI(t, "user2", "2222222222222222")

	// user1 seeds a shared parent and pushes; user2 clones it. Both now
	// see PRJX-1 with zero children — a realistic shared starting point.
	user1.createNode(t, "PRJX-1", "shared parent epic")
	user1.pushAll(ctx, t, pool)
	require.Equal(t, 1, user2.pullAll(ctx, t, pool), "user2 clones the parent")

	// --- Phase 0: collision at birth (UNCHANGED root cause) ----------
	// Two independent local stores, each asked for the next child seq
	// under the SAME parent, still mint the SAME number locally. The fix
	// is NOT in id minting — it is in the hub serializing the settle.
	const seqKey = "PRJX:PRJX-1"
	s1, err := user1.store.NextSequence(ctx, seqKey)
	require.NoError(t, err)
	s2, err := user2.store.NextSequence(ctx, seqKey)
	require.NoError(t, err)
	require.Equal(t, s1, s2,
		"independent CLIs mint the same next child seq under the same parent")

	// --- Phase 1: each user files a DIFFERENT ticket at the SAME id ---
	const (
		id         = "PRJX-1.1"
		user1Title = "User 1 ticket: fix login timeout"
		user2Title = "User 2 ticket: add CSV export"
	)
	user1.createNode(t, id, user1Title)
	user2.createNode(t, id, user2Title)

	// user1 pushes first and wins PRJX-1.1 (first-writer-wins, ADR-003 §6).
	// user2's push gets a renumber-required outcome the helper drains: it
	// re-claims the next free number (PRJX-1.2) and re-pushes. Neither push
	// hangs and neither node is lost.
	_, c1 := user1.pushAll(ctx, t, pool)
	_, c2 := user2.pushAll(ctx, t, pool)
	assert.Zero(t, c1+c2,
		"a create-collision is resolved by renumber, not a field-level conflict")

	// Everyone pulls everyone.
	user1.pullAll(ctx, t, pool)
	user2.pullAll(ctx, t, pool)

	// --- Phase 2: BOTH tickets preserved at DISTINCT numbers ---------
	// The id set is now exactly parent + TWO children (the loser renumbered
	// to the next free seq), not one — no silent loss (ADR-003 §6, §11).
	require.Equal(t, []string{"PRJX-1", "PRJX-1.1", "PRJX-1.2"}, user1.listNodeIDs(t),
		"both children survive at distinct numbers")

	// No split-brain: every replica agrees on the full content.
	assertConverged(t, user1, user2)

	// First writer kept PRJX-1.1; the renumbered loser is PRJX-1.2. Both
	// titles are present and live at distinct ids (looked up by content so
	// the assertion does not depend on push interleaving).
	winnerID := titleByContent(t, user1, user1Title)
	loserID := titleByContent(t, user1, user2Title)
	require.Equal(t, "PRJX-1.1", winnerID, "first writer keeps PRJX-1.1")
	require.Equal(t, "PRJX-1.2", loserID, "renumbered loser settles to next free PRJX-1.2")
	require.NotEqual(t, winnerID, loserID, "the two tickets occupy distinct numbers")

	// --- Phase 3: a fresh clone agrees (no global ambiguity) ---------
	user3 := newFakeCLI(t, "user3", "3333333333333333")
	user3.pullAll(ctx, t, pool)
	assertConverged(t, user1, user2, user3)
	require.Equal(t, user1Title, user3.titleOf(t, "PRJX-1.1"),
		"fresh clone sees the winner at PRJX-1.1")
	require.Equal(t, user2Title, user3.titleOf(t, "PRJX-1.2"),
		"fresh clone sees the renumbered loser at PRJX-1.2")

	// Both tickets reached every replica — neither was silently dropped.
	for _, c := range []*fakeCLI{user1, user2, user3} {
		assert.Contains(t, allTitles(t, c), user1Title, "%s must have user1's ticket", c.name)
		assert.Contains(t, allTitles(t, c), user2Title, "%s must have user2's ticket", c.name)
	}
}

// TestE2E_Collision_NWayConcurrentCreate is the N-way (>=3) corner case of
// ADR-003 §6: several users each create a DIFFERENT ticket at the SAME birth
// id under one parent before syncing. The registry serializes them so all N
// survive at N DISTINCT numbers (1.1..1.N) with no loss and full convergence.
func TestE2E_Collision_NWayConcurrentCreate(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	const n = 4
	clis := make([]*fakeCLI, n)
	for i := range clis {
		clis[i] = newFakeCLI(t, "u"+strconv.Itoa(i+1),
			strconv.Itoa(i+1)+"000000000000000")
	}

	// clis[0] seeds the shared parent; everyone clones it.
	clis[0].createNode(t, "PRJX-1", "shared parent")
	clis[0].pushAll(ctx, t, pool)
	for _, c := range clis[1:] {
		require.Equal(t, 1, c.pullAll(ctx, t, pool), "%s clones the parent", c.name)
	}

	// Each CLI independently mints PRJX-1.1 with a DISTINCT title.
	titles := make([]string, n)
	for i, c := range clis {
		titles[i] = "ticket from " + c.name
		c.createNode(t, "PRJX-1.1", titles[i])
	}

	// Push in order: first wins 1.1, the rest each drain a renumber-required
	// to the next free number (1.2, 1.3, 1.4) and re-push.
	for _, c := range clis {
		c.pushAll(ctx, t, pool)
	}
	// Everyone pulls everyone.
	for _, c := range clis {
		c.pullAll(ctx, t, pool)
	}

	// All N tickets survive at N distinct numbers, fully converged.
	wantIDs := []string{"PRJX-1", "PRJX-1.1", "PRJX-1.2", "PRJX-1.3", "PRJX-1.4"}
	require.Equal(t, wantIDs, clis[0].listNodeIDs(t),
		"all %d concurrent creates survive at distinct numbers", n)
	assertConverged(t, clis...)

	// Every distinct title is present exactly once (no loss, no duplication).
	got := allTitles(t, clis[0])
	sort.Strings(got)
	want := append([]string{"shared parent"}, titles...)
	sort.Strings(want)
	require.Equal(t, want, got, "every ticket preserved exactly once across the renumber")
}
