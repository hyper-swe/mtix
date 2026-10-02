// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"errors"
	"fmt"
	"strings"
)

// noteWorkflowConflicts records in the report the conflicts of a merge
// refused over workflow values, so the caller can print them
// (MTIX-95.31.13).
func (r *ImportReconcileReport) noteWorkflowConflicts(err error) {
	var conflict *WorkflowConflictError
	if errors.As(err, &conflict) {
		r.WorkflowConflicts = conflict.Conflicts
		r.Idempotent -= countIdempotent(conflict.Conflicts)
	}
}

// writeWorkflowConflicts renders the workflow conflicts: per task, every
// differing field with the local and the file's value and the activity
// hint, and, for a merge not applied, how to choose (MTIX-95.31.13).
func writeWorkflowConflicts(b *strings.Builder, conflicts []WorkflowConflict, applied bool) {
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
		}
		fmt.Fprintf(b, "        history (a hint, not a verdict): %s\n", c.Hint)
	}
	if !applied {
		b.WriteString("  not applied: nothing was written. Decide per task which values to keep, then rerun the " +
			"import with --prefer theirs (take the file's) or --prefer ours (keep yours) for all of them, or " +
			"--theirs ID,ID and --ours ID,ID for specific tasks\n")
	}
}

// shownValue renders an empty workflow value as (none).
func shownValue(v string) string {
	if v == "" {
		return "(none)"
	}
	return v
}

// countIdempotent counts the conflicts that the reconcile had counted as
// idempotent no-ops: a task that differs is not a no-op (MTIX-95.31.13).
func countIdempotent(conflicts []WorkflowConflict) int {
	n := 0
	for i := range conflicts {
		if conflicts[i].idempotent {
			n++
		}
	}
	return n
}
