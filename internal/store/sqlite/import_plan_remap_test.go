// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-2.3.2.2: buildRemap rewrites plan_remap in one transaction: a failure part way leaves
// the pairs as they were.
func TestBuildRemap_FailurePartWay_LeavesThePairsAsTheyWere(t *testing.T) {
	ctx := context.Background()
	s, st := planFixture(t)
	plan := &ImportPlan{s: s, st: st, q: st.db}
	plan.counts = planFixtureCounts(t, st)
	_, err := plan.buildRemap(ctx, true)
	require.NoError(t, err)
	before := stagedRemapLines(t, st)
	require.NotEmpty(t, before)

	// A row whose adopted uid the table refuses: the last insert of buildRemap fails.
	_, err = st.db.ExecContext(ctx, `CREATE TRIGGER plan_remap_refuse BEFORE INSERT ON plan_remap
		WHEN NEW.uid = 'refused' BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	require.NoError(t, err)
	_, err = st.db.ExecContext(ctx, `INSERT INTO plan_rows (sec, ord, c1, c2, c3, c4, c5, n)
		VALUES (?, 100000, 'BIG-1', 'refused', '', '', '', 0)`, int(secAdoption))
	require.NoError(t, err)
	_, err = plan.buildRemap(ctx, true)
	require.Error(t, err)
	assert.Equal(t, before, stagedRemapLines(t, st))
}

func planFixtureCounts(t *testing.T, st *StagedExport) [secCount]int {
	t.Helper()
	var counts [secCount]int
	for sec := range counts {
		require.NoError(t, st.db.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM plan_rows WHERE sec = ?`, sec).Scan(&counts[sec]))
	}
	return counts
}

func stagedRemapLines(t *testing.T, st *StagedExport) []string {
	t.Helper()
	rows, err := st.db.QueryContext(context.Background(), `SELECT k || ' ' || uid || ' ' || path FROM plan_remap ORDER BY k`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		out = append(out, line)
	}
	require.NoError(t, rows.Err())
	return out
}

// A staged export is planned once: the second plan is refused by name, and the
// first plan still reports.
func TestPlanImportStaged_PlannedTwice_SecondRefusedFirstStillReports(t *testing.T) {
	s, st := planFixture(t)
	second, err := s.PlanImportStaged(context.Background(), st, planMemoryOptions())
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Nil(t, second)
	has, err := (&ImportPlan{s: s, st: st, q: st.db, counts: planFixtureCounts(t, st)}).HasRemap()
	require.NoError(t, err)
	assert.True(t, has)
}
