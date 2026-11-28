// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Bounded-memory import, slice 2 (MTIX-2.3.2.2, FR-7.8). One formatter writes
// the reconciliation report and the uid-keyed remap from a reportView: the
// in-memory ImportReconcileReport (String, the format every caller knows) and
// the disk-backed ImportPlan both feed it, so the staged report is the current
// report by construction, and neither is ever built as one string.

// reportSection names one list of the report.
type reportSection int

const (
	secConflict      reportSection = iota // c: uid, import path, local path; n: kind
	secRemap                              // c: uid, old path, new path; n: 1 for a minted uid
	secRenamed                            // same columns as secRemap
	secLocalRenumber                      // same columns as secRemap
	secMoved                              // same columns as secRemap
	secAdoption                           // c: id, local uid, file uid, local title, file title
	secTitle                              // c: id, local title, file title
	secWorkflow                           // read through eachWorkflow
	secCount
)

// reportRow is one entry of a report list, as the columns its section names.
type reportRow struct {
	c [5]string
	n int
}

// reportView is what the report formatter reads: the lists in report order,
// one entry at a time.
type reportView interface {
	isApplied() bool
	idempotentCount() int
	rowCount(sec reportSection) int
	eachRow(sec reportSection, fn func(reportRow) error) error
	eachWorkflow(fn func(*WorkflowConflict) error) error
	// eachRemapPair calls fn for every uid-keyed remap pair in uid order,
	// the last entry of a uid winning, as the remap file lists them.
	eachRemapPair(applied bool, fn func(uid, path string) error) error
	// remapEntries counts the entries a remap file is written for (before
	// entries without a uid are left out): none means no file.
	remapEntries(applied bool) (int, error)
}

// ReportSource is a reconciliation report the CLI prints and writes without
// building it in memory: ImportReconcileReport and ImportPlan implement it.
type ReportSource interface {
	// WriteReport writes the human-readable report (ImportReconcileReport.String).
	WriteReport(w io.Writer) error
	// HasRemap reports whether a remap file is written for the report.
	HasRemap() (bool, error)
	// WriteRemap writes the uid-keyed remap as the remap file holds it.
	WriteRemap(w io.Writer) error
	// WriteWorkflowJSON writes the --json document of an unapplied report
	// whose workflow conflicts need a choice, or nothing when it has none.
	WriteWorkflowJSON(w io.Writer) error
	// IsApplied reports whether the import was committed.
	IsApplied() bool
}

// errWriter keeps the first write error so the formatter need not check each line.
type errWriter struct {
	w   io.Writer
	err error
}

// Write implements io.Writer, doing nothing after the first failure.
func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	n, err := e.w.Write(p)
	e.err = err
	return n, err
}

// WriteString writes s.
func (e *errWriter) WriteString(s string) { _, _ = e.Write([]byte(s)) }

// writeReport writes the report of v (the text of ImportReconcileReport.String).
func writeReport(w io.Writer, v reportView) error {
	ew := &errWriter{w: w}
	fmt.Fprintf(ew, "import reconciliation report (applied=%t):\n", v.isApplied())
	fmt.Fprintf(ew, "  idempotent no-ops: %d\n", v.idempotentCount())
	err := firstError(
		func() error { return writeConflictSection(ew, v) },
		func() error { return writeRemapSections(ew, v) },
		func() error { return writeLocalSections(ew, v) },
		func() error { return writeAdoptionSection(ew, v) },
		func() error { return writeWorkflowSection(ew, v) },
	)
	if err != nil {
		return err
	}
	return ew.err
}

// firstError runs the steps in order and returns the first error.
func firstError(steps ...func() error) error {
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func writeConflictSection(ew *errWriter, v reportView) error {
	if n := v.rowCount(secConflict); n > 0 {
		fmt.Fprintf(ew, "  REJECTED uid conflicts: %d\n", n)
		return v.eachRow(secConflict, func(r reportRow) error {
			fmt.Fprintf(ew, "    - uid=%s import=%s local=%s (%s)\n", r.c[0], r.c[1], r.c[2], ImportConflictKind(r.n))
			return ew.err
		})
	}
	return nil
}

func writeRemapSections(ew *errWriter, v reportView) error {
	if n := v.rowCount(secRemap); n > 0 {
		fmt.Fprintf(ew, "  renumbered provisional nodes: %d\n", n)
		err := v.eachRow(secRemap, func(r reportRow) error {
			fmt.Fprintf(ew, "    - uid=%s %s -> %s\n", r.c[0], r.c[1], r.c[2])
			return ew.err
		})
		if err != nil {
			return err
		}
	}
	if n := v.rowCount(secRenamed); n > 0 {
		fmt.Fprintf(ew, "  force-renamed (re-stamped) nodes: %d\n", n)
		return v.eachRow(secRenamed, func(r reportRow) error {
			fmt.Fprintf(ew, "    - %s re-stamped (was uid=%s)\n", r.c[2], r.c[0])
			return ew.err
		})
	}
	return nil
}

func writeLocalSections(ew *errWriter, v reportView) error {
	if n := v.rowCount(secLocalRenumber); n > 0 {
		fmt.Fprintf(ew, "  local tasks renumbered, the file holds a different task under their id: %d\n", n)
		err := v.eachRow(secLocalRenumber, func(r reportRow) error {
			uid := r.c[0]
			if r.n != 0 {
				uid = "(new)" // MTIX-95.31.9: minted by this import
			}
			fmt.Fprintf(ew, "    - uid=%s %s -> %s\n", uid, r.c[1], r.c[2])
			return ew.err
		})
		if err != nil {
			return err
		}
		if !v.isApplied() {
			ew.WriteString("  not applied: review the renumbering above, then rerun the import with --confirm\n")
		}
	}
	if err := writeTitleSection(ew, v); err != nil { // MTIX-95.31.9
		return err
	}
	if n := v.rowCount(secMoved); n > 0 {
		fmt.Fprintf(ew, "  local tasks moved to the id the file holds them under (renumbered elsewhere): %d\n", n)
		return v.eachRow(secMoved, func(r reportRow) error {
			fmt.Fprintf(ew, "    - uid=%s %s -> %s\n", r.c[0], r.c[1], r.c[2])
			return ew.err
		})
	}
	return nil
}

// writeTitleSection renders, after the renumbered local tasks, those
// renumbered because the file holds their id with another title and no uid
// to compare, with both titles (MTIX-95.31.9).
func writeTitleSection(ew *errWriter, v reportView) error {
	n := v.rowCount(secTitle)
	if n == 0 {
		return nil
	}
	fmt.Fprintf(ew, "  of these, a task under this id with a different title and no uid to compare: %d\n", n)
	return v.eachRow(secTitle, func(r reportRow) error {
		fmt.Fprintf(ew, "    - %s (local %q, file %q)\n", r.c[0], r.c[1], r.c[2])
		return ew.err
	})
}

// writeAdoptionSection renders the uid adoptions (MTIX-95.31.6): each one with
// the id, both uids and both titles, and, when the titles of one differ, that
// the two may be different tasks and where the local one is kept.
func writeAdoptionSection(ew *errWriter, v reportView) error {
	n := v.rowCount(secAdoption)
	if n == 0 {
		return nil
	}
	fmt.Fprintf(ew, "  uids adopted from the file (the same task under another uid): %d\n", n)
	titlesDiffer := false
	err := v.eachRow(secAdoption, func(r reportRow) error {
		local := r.c[1]
		if local == "" {
			local = "(new)" // the merge gave the task a backfill uid first (stampMissingUIDs)
		}
		fmt.Fprintf(ew, "    - %s local uid=%s -> file uid=%s (local %q, file %q)\n", r.c[0], local, r.c[2], r.c[3], r.c[4])
		titlesDiffer = titlesDiffer || r.c[3] != r.c[4]
		return ew.err
	})
	if err != nil {
		return err
	}
	if titlesDiffer {
		ew.WriteString("  a task above whose titles differ may be two different tasks created in the same second " +
			"whose create event at least one clone's event log lacks (for example, created before 0.2): the " +
			"merge keeps the file's under the id, and the backup taken before the merge holds the local one\n")
	}
	return nil
}

// writeRemapJSON writes the remap pairs of v as json.MarshalIndent writes a
// map[string]string: sorted keys, two-space indent, no final newline.
func writeRemapJSON(w io.Writer, v reportView, applied bool) error {
	ew := &errWriter{w: w}
	count := 0
	err := v.eachRemapPair(applied, func(uid, path string) error {
		key, kerr := json.Marshal(uid)
		val, verr := json.Marshal(path)
		if err := firstError(func() error { return kerr }, func() error { return verr }); err != nil {
			return fmt.Errorf("encode remap file: %w", err)
		}
		sep := "{\n"
		if count > 0 {
			sep = ",\n"
		}
		count++
		fmt.Fprintf(ew, "%s  %s: %s", sep, key, val)
		return ew.err
	})
	if err != nil {
		return err
	}
	if count == 0 {
		ew.WriteString("{}")
	} else {
		ew.WriteString("\n}")
	}
	return ew.err
}

// writeWorkflowJSONOf writes {"applied":false,"workflow_conflicts":[...]}
// and a newline, as json.Encoder writes the struct, one conflict at a time.
func writeWorkflowJSONOf(w io.Writer, v reportView) error {
	if v.isApplied() || v.rowCount(secWorkflow) == 0 {
		return nil
	}
	ew := &errWriter{w: w}
	ew.WriteString(`{"applied":false,"workflow_conflicts":[`)
	first := true
	err := v.eachWorkflow(func(c *WorkflowConflict) error {
		raw, err := json.Marshal(c)
		if err != nil {
			return fmt.Errorf("encode workflow conflict: %w", err)
		}
		if !first {
			ew.WriteString(",")
		}
		first = false
		_, _ = ew.Write(raw)
		return ew.err
	})
	if err != nil {
		return err
	}
	ew.WriteString("]}\n")
	return ew.err
}

// sortedRemapPairs lists pairs in uid order, the last pair of a uid winning,
// leaving out pairs without a uid.
func sortedRemapPairs(pairs [][2]string) [][2]string {
	last := make(map[string]string, len(pairs))
	for _, p := range pairs {
		if p[0] != "" {
			last[p[0]] = p[1]
		}
	}
	out := make([][2]string, 0, len(last))
	for uid, path := range last {
		out = append(out, [2]string{uid, path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// String renders a loud, human-readable summary of the reconciliation
// (ADR-003 §6). Every conflict, remap, re-stamp and uid adoption
// (MTIX-95.31.6) is listed so the operator can audit exactly what the import
// did or would do.
func (r *ImportReconcileReport) String() string {
	var b strings.Builder
	_ = writeReport(&b, r) // a strings.Builder never fails to write
	return b.String()
}
