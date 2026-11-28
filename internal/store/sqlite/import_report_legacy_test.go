// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// MTIX-2.3.2.2: the report text before the streaming formatter, kept verbatim
// (renamed) as the golden the formatter must equal for every report shape.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/format"
)

func legacyString(r *ImportReconcileReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "import reconciliation report (applied=%t):\n", r.Applied)
	fmt.Fprintf(&b, "  idempotent no-ops: %d\n", r.Idempotent)
	if len(r.Conflicts) > 0 {
		fmt.Fprintf(&b, "  REJECTED uid conflicts: %d\n", len(r.Conflicts))
		for _, c := range r.Conflicts {
			fmt.Fprintf(&b, "    - uid=%s import=%s local=%s (%s)\n",
				c.UID, c.ImportPath, c.LocalPath, c.Kind)
		}
	}
	if len(r.Remaps) > 0 {
		fmt.Fprintf(&b, "  renumbered provisional nodes: %d\n", len(r.Remaps))
		for _, m := range r.Remaps {
			fmt.Fprintf(&b, "    - uid=%s %s -> %s\n", m.UID, m.OldPath, m.NewPath)
		}
	}
	if len(r.Renamed) > 0 {
		fmt.Fprintf(&b, "  force-renamed (re-stamped) nodes: %d\n", len(r.Renamed))
		for _, m := range r.Renamed {
			fmt.Fprintf(&b, "    - %s re-stamped (was uid=%s)\n", m.NewPath, m.UID)
		}
	}
	if len(r.LocalRenumbers) > 0 {
		fmt.Fprintf(&b, "  local tasks renumbered, the file holds a different task under their id: %d\n",
			len(r.LocalRenumbers))
		for _, m := range r.LocalRenumbers {
			fmt.Fprintf(&b, "    - uid=%s %s -> %s\n", legacyShownUID(m), m.OldPath, m.NewPath) // MTIX-95.31.9
		}
		if !r.Applied {
			b.WriteString("  not applied: review the renumbering above, then rerun the import with --confirm\n")
		}
	}
	legacyTitleMismatches(&b, r.TitleMismatches) // MTIX-95.31.9
	if len(r.Moved) > 0 {
		fmt.Fprintf(&b, "  local tasks moved to the id the file holds them under (renumbered elsewhere): %d\n",
			len(r.Moved))
		for _, m := range r.Moved {
			fmt.Fprintf(&b, "    - uid=%s %s -> %s\n", m.UID, m.OldPath, m.NewPath)
		}
	}
	legacyAdoptions(&b, r.UIDAdoptions)                // MTIX-95.31.6
	legacyWorkflow(&b, r.WorkflowConflicts, r.Applied) // MTIX-95.31.13
	return b.String()
}

func legacyShownUID(m ImportRemapEntry) string {
	if m.NewUID {
		return "(new)"
	}
	return m.UID
}

func legacyTitleMismatches(b *strings.Builder, mismatches []ImportTitleMismatch) {
	if len(mismatches) == 0 {
		return
	}
	fmt.Fprintf(b, "  of these, a task under this id with a different title and no uid to compare: %d\n",
		len(mismatches))
	for _, m := range mismatches {
		fmt.Fprintf(b, "    - %s (local %q, file %q)\n", m.ID, m.LocalTitle, m.FileTitle)
	}
}

func legacyAdoptions(b *strings.Builder, adoptions []ImportUIDAdoption) {
	if len(adoptions) == 0 {
		return
	}
	fmt.Fprintf(b, "  uids adopted from the file (the same task under another uid): %d\n", len(adoptions))
	titlesDiffer := false
	for _, a := range adoptions {
		local := a.LocalUID
		if local == "" {
			local = "(new)" // the merge gave the task a backfill uid first (stampMissingUIDs)
		}
		fmt.Fprintf(b, "    - %s local uid=%s -> file uid=%s (local %q, file %q)\n",
			a.ID, local, a.FileUID, a.LocalTitle, a.FileTitle)
		titlesDiffer = titlesDiffer || a.LocalTitle != a.FileTitle
	}
	if titlesDiffer {
		b.WriteString("  a task above whose titles differ may be two different tasks created in the same second " +
			"whose create event at least one clone's event log lacks (for example, created before 0.2): the " +
			"merge keeps the file's under the id, and the backup taken before the merge holds the local one\n")
	}
}

func legacyWorkflow(b *strings.Builder, conflicts []WorkflowConflict, applied bool) {
	if len(conflicts) == 0 {
		return
	}
	if applied {
		fmt.Fprintf(b, "  workflow values settled by your choice: %d task(s)\n", len(conflicts))
	} else {
		fmt.Fprintf(b, "  WORKFLOW CONFLICTS: %d task(s) have a status, assignee, agent state, wake time or deletion state "+
			"that differs from the file's\n", len(conflicts))
	}
	for i := range conflicts {
		c := &conflicts[i]
		fmt.Fprintf(b, "    - %s %q", c.ID, c.Title)
		if c.Choice != "" {
			fmt.Fprintf(b, " -> kept %s", c.Choice)
		}
		b.WriteString("\n")
		for _, f := range c.Fields {
			fmt.Fprintf(b, "        %s: yours %s, the file's %s\n", f.Field, shownValue(f.Local), shownValue(f.File))
			legacyHistory(b, "yours", f.LocalHistory, f.LocalHistoryMore)
			legacyHistory(b, "file", f.FileHistory, f.FileHistoryMore)
		}
		fmt.Fprintf(b, "        history (a hint, not a verdict): %s\n", format.SafeLine(c.Hint))
	}
	if !applied {
		b.WriteString("  not applied: nothing was written. Decide per task which values to keep, then rerun the " +
			"import with --prefer theirs (take the file's) or --prefer ours (keep yours) for all of them, or " +
			"--theirs ID,ID and --ours ID,ID for specific tasks\n")
	}
}

func legacyHistory(b *strings.Builder, side string, entries []exportHistoryEntry, more int) {
	fmt.Fprintf(b, "        %s history (entries outside shared history):\n", side)
	if len(entries) == 0 {
		b.WriteString("          no entries supplied outside shared history; not proof of no changes\n")
	}
	for _, e := range entries {
		fmt.Fprintf(b, "          %q %q by %q (%q, %q)", e.TS, e.ID, e.Actor, e.Origin, e.Kind)
		if e.Old != nil {
			fmt.Fprintf(b, " old=%q", *e.Old)
		}
		if e.New != nil {
			fmt.Fprintf(b, " new=%q", *e.New)
		}
		b.WriteString("\n")
	}
	if more > 0 {
		fmt.Fprintf(b, "          %d more\n", more)
	}
}

// legacyRemap is the remap file body writeRemapFile built before MTIX-2.3.2.2,
// or "" when it wrote no file.
func legacyRemap(report *ImportReconcileReport) (string, error) {
	var moved []ImportRemapEntry
	for _, list := range [][]ImportRemapEntry{report.Remaps, report.LocalRenumbers, report.Moved} {
		for _, m := range list {
			if report.Applied || !m.NewUID {
				moved = append(moved, m)
			}
		}
	}
	for _, a := range report.UIDAdoptions {
		if a.LocalUID != "" {
			moved = append(moved, ImportRemapEntry{UID: a.LocalUID, OldPath: a.ID, NewPath: a.ID})
		}
	}
	if len(moved) == 0 {
		return "", nil
	}
	remap := make(map[string]string, len(moved))
	for _, m := range moved {
		if m.UID != "" {
			remap[m.UID] = m.NewPath
		}
	}
	out, err := json.MarshalIndent(remap, "", "  ")
	return string(out), err
}

// legacyWorkflowJSON is the --json document of an unapplied workflow refusal.
func legacyWorkflowJSON(report *ImportReconcileReport) (string, error) {
	if report.Applied || len(report.WorkflowConflicts) == 0 {
		return "", nil
	}
	var b strings.Builder
	err := json.NewEncoder(&b).Encode(struct {
		Applied   bool               `json:"applied"`
		Conflicts []WorkflowConflict `json:"workflow_conflicts"`
	}{Applied: false, Conflicts: report.WorkflowConflicts})
	return b.String(), err
}
