// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// The ImportPlan as a reportView and ReportSource: the report and the remap are
// read back from the plan rows a page at a time and streamed to the writer.

// reportPageRows is how many plan rows are read at a time.
const reportPageRows = 128

func (p *ImportPlan) isApplied() bool { return p.applied }

// workflowShown reports whether the workflow conflicts belong to the report:
// the merge was refused over them, or it applied with the choices made.
func (p *ImportPlan) workflowShown() bool { return p.wfShown || p.applied }

func (p *ImportPlan) idempotentCount() int {
	if p.workflowShown() {
		return p.idempotent - p.wfIdempotent
	}
	return p.idempotent
}

func (p *ImportPlan) rowCount(sec reportSection) int {
	if sec == secWorkflow && !p.workflowShown() {
		return 0
	}
	return p.counts[sec]
}

// eachPlanRow calls fn for the rows of a section in report order, reading
// reportPageRows at a time.
func (p *ImportPlan) eachPlanRow(ctx context.Context, sec reportSection, fn func(reportRow) error) error {
	for after := -1; ; {
		page, err := p.readPlanPage(ctx, sec, after)
		if err != nil {
			return err
		}
		for _, r := range page {
			if err := fn(r); err != nil {
				return err
			}
		}
		if len(page) < reportPageRows {
			return nil
		}
		after += len(page)
	}
}

// readPlanPage reads one page of a section's rows after place after, and
// closes the cursor before returning.
func (p *ImportPlan) readPlanPage(ctx context.Context, sec reportSection, after int) ([]reportRow, error) {
	// A page of one report list, in order.
	rows, err := p.q.QueryContext(ctx, `SELECT c1, c2, c3, c4, c5, n FROM plan_rows
		WHERE sec = ? AND ord > ? ORDER BY ord LIMIT ?`, int(sec), after, reportPageRows)
	if err != nil {
		return nil, fmt.Errorf("read the import plan: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var page []reportRow
	for rows.Next() {
		var r reportRow
		if err := rows.Scan(&r.c[0], &r.c[1], &r.c[2], &r.c[3], &r.c[4], &r.n); err != nil {
			return nil, fmt.Errorf("read the import plan: %w", err)
		}
		page = append(page, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the import plan: %w", err)
	}
	return page, nil
}

func (p *ImportPlan) eachRow(sec reportSection, fn func(reportRow) error) error {
	return p.eachPlanRow(context.Background(), sec, fn)
}

func (p *ImportPlan) eachWorkflow(fn func(*WorkflowConflict) error) error {
	return p.eachPlanRow(context.Background(), secWorkflow, func(r reportRow) error {
		var c WorkflowConflict
		if err := json.Unmarshal([]byte(r.c[0]), &c); err != nil {
			return fmt.Errorf("decode workflow conflict: %w", err)
		}
		c.Choice = WorkflowChoice(r.c[1])
		return fn(&c)
	})
}

// buildRemap fills plan_remap with the pairs a remap file is written for, in
// the order of ImportReconcileReport.remapPairs, and returns how many.
func (p *ImportPlan) buildRemap(ctx context.Context, applied bool) (int, error) {
	if _, err := p.q.ExecContext(ctx, `DELETE FROM plan_remap`); err != nil {
		return 0, fmt.Errorf("clear the remap pairs: %w", err)
	}
	total := 0
	for _, sec := range []reportSection{secRemap, secLocalRenumber, secMoved} {
		// A minted uid is listed only once the import is applied (MTIX-95.31.9).
		res, err := p.q.ExecContext(ctx, `INSERT INTO plan_remap (uid, path)
			SELECT c1, c3 FROM plan_rows WHERE sec = ? AND (? OR n = 0) ORDER BY ord`, int(sec), applied)
		if err != nil {
			return 0, fmt.Errorf("collect the remap pairs: %w", err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	// The local uids a merge gave up map to the id their task keeps (MTIX-95.31.6).
	res, err := p.q.ExecContext(ctx, `INSERT INTO plan_remap (uid, path)
		SELECT c2, c1 FROM plan_rows WHERE sec = ? AND c2 <> '' ORDER BY ord`, int(secAdoption))
	if err != nil {
		return 0, fmt.Errorf("collect the adopted uids: %w", err)
	}
	n, _ := res.RowsAffected()
	return total + int(n), nil
}

func (p *ImportPlan) remapEntries(applied bool) (int, error) {
	return p.buildRemap(context.Background(), applied)
}

func (p *ImportPlan) eachRemapPair(applied bool, fn func(uid, path string) error) error {
	ctx := context.Background()
	if _, err := p.buildRemap(ctx, applied); err != nil {
		return err
	}
	for last := ""; ; {
		pairs, err := p.readRemapPage(ctx, last)
		if err != nil {
			return err
		}
		for _, pair := range pairs {
			if err := fn(pair[0], pair[1]); err != nil {
				return err
			}
			last = pair[0]
		}
		if len(pairs) < reportPageRows {
			return nil
		}
	}
}

// readRemapPage reads the next pairs after uid last, one per uid, the last
// entry of a uid winning, in uid order.
func (p *ImportPlan) readRemapPage(ctx context.Context, last string) ([][2]string, error) {
	// One pair per uid: the path of its last entry.
	rows, err := p.q.QueryContext(ctx, `SELECT uid, path, MAX(k) FROM plan_remap
		WHERE uid <> '' AND uid > ? GROUP BY uid ORDER BY uid LIMIT ?`, last, reportPageRows)
	if err != nil {
		return nil, fmt.Errorf("read the remap pairs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var pairs [][2]string
	for rows.Next() {
		var pair [2]string
		var k int
		if err := rows.Scan(&pair[0], &pair[1], &k); err != nil {
			return nil, fmt.Errorf("read the remap pairs: %w", err)
		}
		pairs = append(pairs, pair)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the remap pairs: %w", err)
	}
	return pairs, nil
}

// WriteReport writes the report text, the text ImportReconcileReport.String
// returns for the same reconciliation, without building it in memory.
func (p *ImportPlan) WriteReport(w io.Writer) error { return writeReport(w, p) }

// HasRemap reports whether a remap file is written for the plan.
func (p *ImportPlan) HasRemap() (bool, error) {
	n, err := p.remapEntries(p.applied)
	return n > 0, err
}

// WriteRemap writes the uid-keyed remap (uid -> new display_path).
func (p *ImportPlan) WriteRemap(w io.Writer) error { return writeRemapJSON(w, p, p.applied) }

// WriteWorkflowJSON writes the --json document of an unapplied plan whose
// workflow conflicts need a choice.
func (p *ImportPlan) WriteWorkflowJSON(w io.Writer) error { return writeWorkflowJSONOf(w, p) }
