// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import "io"

// The in-memory ImportReconcileReport as a reportView and ReportSource.

func (r *ImportReconcileReport) isApplied() bool { return r.Applied }

func (r *ImportReconcileReport) idempotentCount() int { return r.Idempotent }

// IsApplied reports whether the import was committed.
func (r *ImportReconcileReport) IsApplied() bool { return r.Applied }

func (r *ImportReconcileReport) rowCount(sec reportSection) int {
	switch sec {
	case secConflict:
		return len(r.Conflicts)
	case secRemap:
		return len(r.Remaps)
	case secRenamed:
		return len(r.Renamed)
	case secLocalRenumber:
		return len(r.LocalRenumbers)
	case secMoved:
		return len(r.Moved)
	case secAdoption:
		return len(r.UIDAdoptions)
	case secTitle:
		return len(r.TitleMismatches)
	case secWorkflow:
		return len(r.WorkflowConflicts)
	default:
		return 0
	}
}

func remapRows(list []ImportRemapEntry, fn func(reportRow) error) error {
	for _, m := range list {
		row := reportRow{c: [5]string{m.UID, m.OldPath, m.NewPath}}
		if m.NewUID {
			row.n = 1
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return nil
}

func (r *ImportReconcileReport) eachRow(sec reportSection, fn func(reportRow) error) error {
	switch sec {
	case secConflict:
		for _, c := range r.Conflicts {
			if err := fn(reportRow{c: [5]string{c.UID, c.ImportPath, c.LocalPath}, n: int(c.Kind)}); err != nil {
				return err
			}
		}
	case secRemap:
		return remapRows(r.Remaps, fn)
	case secRenamed:
		return remapRows(r.Renamed, fn)
	case secLocalRenumber:
		return remapRows(r.LocalRenumbers, fn)
	case secMoved:
		return remapRows(r.Moved, fn)
	case secAdoption:
		for _, a := range r.UIDAdoptions {
			if err := fn(reportRow{c: [5]string{a.ID, a.LocalUID, a.FileUID, a.LocalTitle, a.FileTitle}}); err != nil {
				return err
			}
		}
	case secTitle:
		for _, m := range r.TitleMismatches {
			if err := fn(reportRow{c: [5]string{m.ID, m.LocalTitle, m.FileTitle}}); err != nil {
				return err
			}
		}
	case secWorkflow, secCount:
	}
	return nil
}

func (r *ImportReconcileReport) eachWorkflow(fn func(*WorkflowConflict) error) error {
	for i := range r.WorkflowConflicts {
		if err := fn(&r.WorkflowConflicts[i]); err != nil {
			return err
		}
	}
	return nil
}

// remapPairs lists the entries a remap file is written for, in the order the
// plan made them: renumbered provisional nodes, renumbered local tasks, moved
// local tasks, then the local uids a merge gave up (MTIX-95.31.6). A uid the
// import minted (NewUID) is listed only once the import is applied
// (MTIX-95.31.9).
func (r *ImportReconcileReport) remapPairs(applied bool) [][2]string {
	var pairs [][2]string
	for _, list := range [][]ImportRemapEntry{r.Remaps, r.LocalRenumbers, r.Moved} {
		for _, m := range list {
			if applied || !m.NewUID {
				pairs = append(pairs, [2]string{m.UID, m.NewPath})
			}
		}
	}
	for _, a := range r.UIDAdoptions {
		if a.LocalUID != "" {
			pairs = append(pairs, [2]string{a.LocalUID, a.ID})
		}
	}
	return pairs
}

func (r *ImportReconcileReport) remapEntries(applied bool) (int, error) {
	return len(r.remapPairs(applied)), nil
}

func (r *ImportReconcileReport) eachRemapPair(applied bool, fn func(uid, path string) error) error {
	for _, p := range sortedRemapPairs(r.remapPairs(applied)) {
		if err := fn(p[0], p[1]); err != nil {
			return err
		}
	}
	return nil
}

// WriteReport writes the report text, as String returns it.
func (r *ImportReconcileReport) WriteReport(w io.Writer) error { return writeReport(w, r) }

// HasRemap reports whether a remap file is written for the report.
func (r *ImportReconcileReport) HasRemap() (bool, error) {
	n, err := r.remapEntries(r.Applied)
	return n > 0, err
}

// WriteRemap writes the uid-keyed remap (uid -> new display_path).
func (r *ImportReconcileReport) WriteRemap(w io.Writer) error { return writeRemapJSON(w, r, r.Applied) }

// WriteWorkflowJSON writes the --json document of an unapplied report whose
// workflow conflicts need a choice.
func (r *ImportReconcileReport) WriteWorkflowJSON(w io.Writer) error {
	return writeWorkflowJSONOf(w, r)
}
