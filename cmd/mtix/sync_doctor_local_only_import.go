// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// localOnlyImportCheckName is the doctor check for imported changes that
// stay local (MTIX-112.17.16.16).
const localOnlyImportCheckName = "local-only imported changes"

// localOnlyRoutes is what to run to send an imported value, by field: the
// edit that journals the field's current value. Each route here was run on
// the real binary and cleared the entry (TestLocalOnlyRoutes_RealBinary).
// A field with no route (wake time, agent state, a deletion) is not
// recorded: it is named in the import notice as staying on this machine.
// The task drops off the list once every listed field has an event after the
// import.
func localOnlyRoutes() map[string]string {
	return map[string]string{
		"title": `mtix update <id> --title "<current title>"`, "description": `mtix update <id> --description "<current>"`,
		"prompt": `mtix update <id> --prompt "<current>"`, "acceptance": `mtix update <id> --acceptance "<current>"`,
		"labels": `mtix update <id> --labels "<current>"`, "priority": "mtix update <id> --priority <current>",
		"assignee": `mtix update <id> --assignee "<current>"`, "issue_type": "mtix update <id> --type <current>",
		"status": "move the task through another status and back, by the status it has now: done: mtix reopen <id>, " +
			"then mtix claim <id> --agent <agent>, then mtix done <id>; cancelled: mtix reopen <id>, then mtix cancel <id>; in_progress: mtix unclaim <id>, " +
			"then mtix claim <id> --agent <assignee>; open: mtix claim <id> --agent <agent>, then mtix unclaim <id>; " +
			"deferred: mtix reopen <id>, then mtix defer <id> (the wake time is not sent); blocked: no route, " +
			"change its dependencies",
		"annotations": `mtix comment <id> "<text>"`,
		"dependencies": "for each dependency: mtix dep remove <from-id> <to-id>, then mtix dep add <from-id> <to-id> " +
			"(add --type for a type other than blocks)",
	}
}

// localOnlyImportFix is what the check prints: the true routes. A value stays
// local until an edit of the same field sends it; a pull replaces it only
// when the hub holds a later edit of that field, so there is no command that
// takes the hub's value on demand.
const localOnlyImportFix = "to keep a listed value and send it: edit that field again, once per field listed in " +
	"'fields' (routes below); the task drops off this list when every listed field has an event after the import. " +
	"To take the hub's value: no command does it on demand; mtix sync pull replaces a local value only when the hub " +
	"holds a later edit of that field, so edit the field back to the hub's value yourself. A wake time, an agent " +
	"state, a deletion, weight, estimates, references and metadata have no route: they stay on this machine and " +
	"are not listed here"

// routesFor lists the edit that sends each field in fields.
func routesFor(fields []string) string {
	seen := map[string]bool{}
	var parts []string
	routes := localOnlyRoutes()
	for _, f := range fields {
		if r, ok := routes[f]; ok && !seen[f] {
			seen[f] = true
			parts = append(parts, f+": "+r)
		}
	}
	return strings.Join(parts, "; ")
}

// appendLocalOnlyImportCheck adds the "local-only imported changes" check: a
// WARN while an automatic import has changed a task the hub already knows and
// nothing since has sent or replaced the changed field. It reads the local
// store only and contacts no hub. With --json the check carries the bounded id
// list in local_only and the full count in its detail, and mtix doctor --json
// carries the ids in the row's data.
func appendLocalOnlyImportCheck(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	live, overflow, err := st.LocalOnlyImports(ctx)
	if err != nil {
		return appendCheck(r, localOnlyImportCheckName, false, "could not read the local-only imports: "+err.Error())
	}
	total := len(live) + overflow
	if total == 0 {
		return appendCheck(r, localOnlyImportCheckName, true, "no imported change is waiting local only")
	}
	ids := make([]string, 0, sqlite.MaxNamedNodes)
	named := make([]string, 0, sqlite.MaxNamedNodes)
	var fields []string
	for i := 0; i < len(live) && i < sqlite.MaxNamedNodes; i++ {
		ids = append(ids, live[i].ID)
		named = append(named, live[i].ID+" ("+strings.Join(live[i].Fields, ", ")+")")
		fields = append(fields, live[i].Fields...)
	}
	detail := fmt.Sprintf("%d task(s) the hub knows were changed by an automatic import and the change was not sent to the hub; "+
		"a pull of a hub edit may replace these values. First %d: %s", total, len(ids), strings.Join(named, "; "))
	if overflow > 0 {
		detail += fmt.Sprintf(". %d more task(s) are counted but not listed; the count clears when every listed task has settled", overflow)
	}
	fix := localOnlyImportFix
	if routes := routesFor(fields); routes != "" {
		fix += ". Routes by field: " + routes
	}
	return appendDoctorCheck(r, DoctorCheck{Name: localOnlyImportCheckName, Pass: true, Warn: true,
		Detail: detail, Fix: fix, LocalOnly: ids})
}
