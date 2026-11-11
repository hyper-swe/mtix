// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrUnknownCell marks a measured summary for a cell the matrix does not define.
var ErrUnknownCell = errors.New("unknown matrix cell")

// MinN is the per-cell sample size the acceptance bar asks for.
const MinN = 30

// Status says how a matrix cell is to be filled.
type Status string

// Cell statuses. Unattended cells are measured by the harness; the others
// are recorded explicitly in the report instead of being omitted.
const (
	StatusUnattended    Status = "unattended"
	StatusPending       Status = "pending"
	StatusNotRun        Status = "not-run"
	StatusNotApplicable Status = "n/a"
)

// MatrixCell is one cell of the design section 6 test matrix.
type MatrixCell struct {
	ID     string
	Route  string
	State  string
	Status Status
	Reason string
}

// routes lists the matrix routes (design section 4) as id and display name.
func routes() []struct{ id, name string } {
	return []struct{ id, name string }{
		{"A", "socket push"}, {"B", "hook waiter"}, {"C", "codex queue"}, {"R0", "agent-held fallback"},
	}
}

// states lists the matrix states (design section 6).
func states() []string {
	return []string{"idle", "busy", "restarted", "absent", "gone", "bypass", "permission-mode"}
}

const (
	reasonS4 = "S4 bypass-permissions session: lead hand-run, the harness never launches one"
	reasonS6 = "S6 not run: Codex is at its weekly stop line; follow-up MTIX-112.40.1.1"
)

// Matrix returns every cell of the design section 6 matrix (route by
// state), plus the both-routes-armed and hub-board rows, in report order.
// S4 (bypass) and S6 (Codex) cells carry their pending or not-run reason
// so the report can never silently omit them (MTIX-112.40.1).
func Matrix() []MatrixCell {
	var cells []MatrixCell
	for _, r := range routes() {
		for _, state := range states() {
			st, reason := cellStatus(r.id, state)
			cells = append(cells, MatrixCell{ID: r.id + "/" + state, Route: r.name, State: state, Status: st, Reason: reason})
		}
	}
	cells = append(cells,
		MatrixCell{ID: "AB/both-armed", Route: "socket push + hook waiter", State: "both armed", Status: StatusUnattended},
		MatrixCell{ID: "ALL/hub-board", Route: "all routes", State: "hub board", Status: StatusNotRun,
			Reason: "needs a hub board; the harness holds no hub credentials, so zero-hub-contact is left to hosted CI"},
	)
	return cells
}

func cellStatus(route, state string) (Status, string) {
	switch {
	case route == "C":
		return StatusNotRun, reasonS6
	case state == "bypass":
		return StatusPending, reasonS4
	case route == "R0" && (state == "idle" || state == "absent" || state == "gone"):
		return StatusNotApplicable, "agent-held waiter: no listener to wake or lose"
	}
	return StatusUnattended, ""
}

// Render writes the evidence table: one row per matrix cell, in matrix
// order, with the measured numbers when a summary exists and an explicit
// NOT MEASURED, PENDING, NOT RUN or N/A status otherwise. A summary for a
// cell the matrix does not define is ErrUnknownCell. Output is
// deterministic.
func Render(sums []CellSummary, cells []MatrixCell) (string, error) {
	byCell := make(map[string]CellSummary, len(sums))
	for _, s := range sums {
		byCell[s.Cell] = s
	}
	known := make(map[string]bool, len(cells))
	for _, c := range cells {
		known[c.ID] = true
	}
	var unknown []string
	for id := range byCell {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return "", fmt.Errorf("%w: %s", ErrUnknownCell, strings.Join(unknown, ", "))
	}
	var b strings.Builder
	b.WriteString("| Cell | Route | State | N | Delivered | Lost | Dup | Unmatched | Turn p50 | Turn p95 | Turn max | Exit p50 | Status |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	unmeasured := 0
	for _, c := range cells {
		s, ok := byCell[c.ID]
		if !ok && c.Status == StatusUnattended {
			unmeasured++
		}
		b.WriteString(row(c, s, ok))
	}
	fmt.Fprintf(&b, "\n%d unattended cells not measured.\n", unmeasured)
	return b.String(), nil
}

func row(c MatrixCell, s CellSummary, measured bool) string {
	head := fmt.Sprintf("| %s | %s | %s |", c.ID, c.Route, c.State)
	if !measured {
		return fmt.Sprintf("%s - | - | - | - | - | - | - | - | - | %s |\n", head, missingStatus(c))
	}
	status := "measured"
	if s.Sent < MinN {
		status = fmt.Sprintf("measured, N<%d", MinN)
	}
	if c.Status == StatusPending || c.Status == StatusNotRun {
		status += " (hand-run: " + c.Reason + ")"
	}
	return fmt.Sprintf("%s %d | %d | %d | %d | %d | %s | %s | %s | %s | %s |\n", head,
		s.Sent, s.Delivered, s.Lost, s.Duplicate, s.Unmatched,
		secs(s.Turn.P50), secs(s.Turn.P95), secs(s.Turn.Max), exitP50(s), status)
}

func missingStatus(c MatrixCell) string {
	switch c.Status {
	case StatusPending:
		return "PENDING: " + c.Reason
	case StatusNotRun:
		return "NOT RUN: " + c.Reason
	case StatusNotApplicable:
		return "N/A: " + c.Reason
	}
	return "NOT MEASURED"
}

func exitP50(s CellSummary) string {
	if s.ExitN == 0 {
		return "-"
	}
	return secs(s.Exit.P50)
}

func secs(d time.Duration) string {
	return fmt.Sprintf("%.2fs", d.Seconds())
}
