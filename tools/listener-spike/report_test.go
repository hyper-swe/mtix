// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func cellByID(t *testing.T, id string) MatrixCell {
	t.Helper()
	for _, c := range Matrix() {
		if c.ID == id {
			return c
		}
	}
	require.Failf(t, "cell missing", "matrix has no cell %s", id)
	return MatrixCell{}
}

func rowOf(t *testing.T, report, id string) string {
	t.Helper()
	var rows []string
	for _, line := range strings.Split(report, "\n") {
		if strings.HasPrefix(line, "| "+id+" |") {
			rows = append(rows, line)
		}
	}
	require.Len(t, rows, 1, "cell %s must appear exactly once", id)
	return rows[0]
}

func TestMatrix_EveryRouteAndState_IsPresentOnceWithUniqueIDs(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Matrix() {
		require.False(t, seen[c.ID], "duplicate cell %s", c.ID)
		seen[c.ID] = true
	}
	for _, route := range []string{"A", "B", "C", "R0"} {
		for _, state := range []string{"idle", "busy", "restarted", "absent", "gone", "bypass", "permission-mode"} {
			require.True(t, seen[route+"/"+state], "design section 6 cell %s/%s is missing", route, state)
		}
	}
	require.True(t, seen["AB/both-armed"], "the two-routes-armed duplicate-wake row is missing")
	require.True(t, seen["ALL/hub-board"], "the hub-board row is missing")
}

func TestMatrix_S4AndS6_AreFlaggedNeverOmitted(t *testing.T) {
	for _, id := range []string{"A/bypass", "B/bypass"} {
		c := cellByID(t, id)
		require.Equal(t, StatusPending, c.Status, id)
		require.Contains(t, c.Reason, "S4", id)
	}
	for _, state := range []string{"idle", "busy", "restarted", "absent", "gone", "bypass", "permission-mode"} {
		c := cellByID(t, "C/"+state)
		require.Equal(t, StatusNotRun, c.Status, c.ID)
		require.Contains(t, c.Reason, "S6", c.ID)
		require.Contains(t, c.Reason, "MTIX-112.40.1.1", c.ID)
	}
}

func TestMatrix_UnattendedCells_IncludeTheMeasuredRows(t *testing.T) {
	for _, id := range []string{"A/idle", "A/busy", "A/restarted", "A/absent", "A/gone",
		"B/idle", "B/busy", "B/restarted", "B/absent", "B/gone", "AB/both-armed"} {
		require.Equal(t, StatusUnattended, cellByID(t, id).Status, id)
	}
}

func TestRender_NoMeasurements_EveryCellOnceAndUnmeasuredIsExplicit(t *testing.T) {
	out, err := Render(nil, Matrix())
	require.NoError(t, err)
	for _, c := range Matrix() {
		rowOf(t, out, c.ID)
	}
	require.Contains(t, rowOf(t, out, "B/idle"), "NOT MEASURED")
	require.Contains(t, rowOf(t, out, "A/bypass"), "PENDING")
	require.Contains(t, rowOf(t, out, "A/bypass"), "S4")
	require.Contains(t, rowOf(t, out, "C/idle"), "NOT RUN")
	require.Contains(t, rowOf(t, out, "C/idle"), "S6")
	require.Contains(t, rowOf(t, out, "R0/idle"), "N/A")
	require.Regexp(t, `\d+ unattended cells? not measured`, out)
}

func TestRender_MeasuredCell_ShowsNumbersAndTheBarOnN(t *testing.T) {
	sums := []CellSummary{
		{Cell: "B/idle", Sent: 30, Delivered: 29, Lost: 1, Duplicate: 2, Unmatched: 3,
			Turn:  Percentiles{P50: 940 * time.Millisecond, P95: 2210 * time.Millisecond, Max: 2650 * time.Millisecond},
			ExitN: 29, Exit: Percentiles{P50: 790 * time.Millisecond}},
		{Cell: "A/idle", Sent: 5, Delivered: 5, Turn: Percentiles{P50: time.Second, P95: time.Second, Max: time.Second}},
	}
	out, err := Render(sums, Matrix())
	require.NoError(t, err)
	b := rowOf(t, out, "B/idle")
	for _, want := range []string{"| 30 |", "| 29 |", "0.94s", "2.21s", "2.65s", "0.79s", "measured"} {
		require.Contains(t, b, want)
	}
	require.NotContains(t, b, "NOT MEASURED")
	a := rowOf(t, out, "A/idle")
	require.Contains(t, a, "N<30", "a cell below the N=30 bar must say so")
}

func TestRender_SummaryForPendingCell_IsShownAsHandRunResult(t *testing.T) {
	sums := []CellSummary{{Cell: "B/bypass", Sent: 5, Delivered: 5, Turn: Percentiles{P50: 280 * time.Millisecond}}}
	out, err := Render(sums, Matrix())
	require.NoError(t, err)
	row := rowOf(t, out, "B/bypass")
	require.Contains(t, row, "0.28s")
	require.Contains(t, row, "S4")
}

func TestRender_UnknownCellInSummaries_ReturnsErrUnknownCell(t *testing.T) {
	_, err := Render([]CellSummary{{Cell: "Z/idle", Sent: 1}}, Matrix())
	require.ErrorIs(t, err, ErrUnknownCell)
	require.Contains(t, err.Error(), "Z/idle")
}

func TestRender_SameInput_IsByteIdentical(t *testing.T) {
	sums := []CellSummary{{Cell: "B/idle", Sent: 30, Delivered: 30}}
	one, err := Render(sums, Matrix())
	require.NoError(t, err)
	two, err := Render(sums, Matrix())
	require.NoError(t, err)
	require.Equal(t, one, two)
}

func TestRender_UnmeasuredFooter_CountsExactlyTheUnattendedCellsWithoutSummaries(t *testing.T) {
	unattended := 0
	for _, c := range Matrix() {
		if c.Status == StatusUnattended {
			unattended++
		}
	}
	require.Positive(t, unattended)
	none, err := Render(nil, Matrix())
	require.NoError(t, err)
	require.Contains(t, none, fmt.Sprintf("\n%d unattended cells not measured.\n", unattended))
	one, err := Render([]CellSummary{{Cell: "B/idle", Sent: 30, Delivered: 30}}, Matrix())
	require.NoError(t, err)
	require.Contains(t, one, fmt.Sprintf("\n%d unattended cells not measured.\n", unattended-1))
	// A pending cell with a hand-run summary does not change the count.
	pend, err := Render([]CellSummary{{Cell: "B/bypass", Sent: 5, Delivered: 5}}, Matrix())
	require.NoError(t, err)
	require.Contains(t, pend, fmt.Sprintf("\n%d unattended cells not measured.\n", unattended))
}
