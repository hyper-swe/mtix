// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Reproduction for MTIX-28: concurrent create-under-same-parent collision.
//
// This is a CHARACTERIZATION test — it asserts the CURRENT (buggy)
// behavior so it is GREEN today and pins the defect in place. When
// MTIX-28 is fixed, flip the assertions to the "DESIRED (post-fix)"
// block documented inline.
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

// TestE2E_Collision_ConcurrentCreateUnderSameParent demonstrates that two
// users who each file a ticket under the same parent before syncing both
// mint the SAME id, and the sync layer neither preserves both nor surfaces
// a conflict — the two replicas end up disagreeing about what that id IS.
func TestE2E_Collision_ConcurrentCreateUnderSameParent(t *testing.T) {
	// SUPERSEDED MID-FIX (MTIX-30.4): the hub registry now returns a
	// renumber-required outcome for the second, colliding create instead of
	// silently accepting it (accepted=0). This test's pushAll helper has no
	// way to drain a renumber outcome, so the loop would spin forever. That
	// is evidence the fix is landing, not a regression. MTIX-30.7 owns the
	// rewrite: teach pushAll to handle renumber outcomes and flip these
	// assertions to the DESIRED behavior (both tickets preserved, distinct
	// numbers, all replicas agree). Skipped until then so it cannot hang.
	t.Skip("superseded by MTIX-30.4 registry; rewritten in MTIX-30.7 (see comment)")

	pool := openHub(t)
	ctx := context.Background()

	user1 := newFakeCLI(t, "user1", "1111111111111111")
	user2 := newFakeCLI(t, "user2", "2222222222222222")

	// user1 seeds a shared parent and pushes; user2 clones it. Both now
	// see PRJX-1 with zero children — a realistic shared starting point.
	user1.createNode(t, "PRJX-1", "shared parent epic")
	user1.pushAll(ctx, t, pool)
	require.Equal(t, 1, user2.pullAll(ctx, t, pool), "user2 clones the parent")

	// --- Phase 0: collision at birth --------------------------------
	// Two independent local stores, each asked for the next child seq
	// under the SAME parent, return the SAME number. This is the root
	// cause: sequence assignment is local and uncoordinated (the hub
	// assigns no IDs).
	const seqKey = "PRJX:PRJX-1"
	s1, err := user1.store.NextSequence(ctx, seqKey)
	require.NoError(t, err)
	s2, err := user2.store.NextSequence(ctx, seqKey)
	require.NoError(t, err)
	require.Equal(t, s1, s2,
		"independent CLIs mint the same next child seq under the same parent")
	t.Logf("Phase 0: both CLIs independently minted child seq %d under PRJX-1 -> both will use PRJX-1.%d", s1, s1)

	// --- Phase 1: each user files a DIFFERENT ticket at the SAME id --
	const (
		id         = "PRJX-1.1"
		user1Title = "User 1 ticket: fix login timeout"
		user2Title = "User 2 ticket: add CSV export"
	)
	user1.createNode(t, id, user1Title)
	user2.createNode(t, id, user2Title)

	// Both push. The hub accepts both create_node events (distinct
	// event_ids) and reports ZERO conflicts — the collision is invisible
	// to the conflict machinery (create_node is excluded from
	// detectConflicts).
	_, c1 := user1.pushAll(ctx, t, pool)
	_, c2 := user2.pushAll(ctx, t, pool)
	assert.Zero(t, c1+c2,
		"BUG(MTIX-28): a create-collision produces NO surfaced conflict")

	// Everyone pulls everyone.
	user1.pullAll(ctx, t, pool)
	user2.pullAll(ctx, t, pool)

	// --- Phase 2: the integrity failure -----------------------------
	// id sets converge (both have exactly the parent + one child)...
	require.Equal(t, []string{"PRJX-1", "PRJX-1.1"}, user1.listNodeIDs(t))
	require.Equal(t, user1.listNodeIDs(t), user2.listNodeIDs(t),
		"replicas converge on the same id SET")

	// ...but the CONTENT at PRJX-1.1 diverges: applyCreateNode does
	// INSERT OR IGNORE, so each creator keeps its own row and silently
	// ignores the other's incoming create. Two real tickets, one id, two
	// different truths.
	t1 := user1.titleOf(t, id)
	t2 := user2.titleOf(t, id)
	t.Logf("Phase 2: user1 sees %s = %q", id, t1)
	t.Logf("Phase 2: user2 sees %s = %q", id, t2)

	// CURRENT behavior (this is the bug): each creator keeps its own
	// ticket; the replicas do NOT agree on PRJX-1.1's content.
	assert.Equal(t, user1Title, t1, "BUG(MTIX-28): user1 keeps only its own ticket")
	assert.Equal(t, user2Title, t2, "BUG(MTIX-28): user2 keeps only its own ticket")
	assert.NotEqual(t, t1, t2,
		"BUG(MTIX-28): the two replicas DISAGREE about what PRJX-1.1 is (split content)")

	// A newcomer that clones fresh sees neither user's truth guaranteed —
	// it gets whichever create_node the hub ordered first. So there is no
	// single global answer to "what is PRJX-1.1".
	user3 := newFakeCLI(t, "user3", "3333333333333333")
	user3.pullAll(ctx, t, pool)
	t3 := user3.titleOf(t, id)
	t.Logf("Phase 2: a fresh clone (user3) sees %s = %q", id, t3)
	assert.Contains(t, []string{user1Title, user2Title}, t3,
		"newcomer sees exactly one of the two tickets")
	assert.True(t, t3 != t1 || t3 != t2,
		"BUG(MTIX-28): the newcomer disagrees with at least one creator")

	// One user's ticket is absent from at least one replica entirely.
	assert.NotContains(t, allTitles(t, user1), user2Title,
		"BUG(MTIX-28): user2's ticket never reached user1")
	assert.NotContains(t, allTitles(t, user2), user1Title,
		"BUG(MTIX-28): user1's ticket never reached user2")

	// DESIRED (post-fix, MTIX-28) — flip the assertions above to:
	//   - both tickets preserved: listNodeIDs == [PRJX-1, PRJX-1.1, PRJX-1.2]
	//     (the second create renumbered to the next free seq), OR
	//   - the collision surfaced as a sync_conflicts row resolvable via
	//     `mtix sync conflicts resolve --action both-renumbered`;
	//   - all replicas (incl. the newcomer) agree on identical content.
}
