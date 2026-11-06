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
// edit that journals the field's current value. The task drops off the list
// once every listed field has an event after the import.
var localOnlyRoutes = map[string]string{
	"title": `mtix update <id> --title "<current title>"`, "description": `mtix update <id> --description "<current>"`,
	"prompt": `mtix update <id> --prompt "<current>"`, "acceptance": `mtix update <id> --acceptance "<current>"`,
	"labels": `mtix update <id> --labels "<current>"`, "priority": "mtix update <id> --priority <current>",
	"assignee": `mtix update <id> --assignee "<current>"`, "issue_type": "mtix update <id> --type <current>",
	"status":      "mtix claim / done / defer / cancel / reopen <id> (the command for the status it has now)",
	"defer_until": "mtix defer <id> --until <current wake time>",
	"annotations": `mtix comment <id> "<text>"`, "dependencies": "mtix dep add <from-id> <to-id> (or mtix dep remove)",
	"deleted_at":  "mtix delete <id>",
	"agent_state": "mtix agent state <agent-id> (for the agent that holds the task)",
}

// localOnlyImportFix is what the check prints: the true routes. A value stays
// local until an edit of the same field sends it; a pull replaces it only
// when the hub holds a later edit of that field, so there is no command that
// takes the hub's value on demand.
const localOnlyImportFix = "to keep a listed value and send it: edit that field again, once per field listed in " +
	"'fields' (title: mtix update <id> --title \"<current title>\"; other fields: see the routes below); the task " +
	"drops off this list when every listed field has an event after the import. To take the hub's value: no command " +
	"does it on demand; mtix sync pull replaces a local value only when the hub holds a later edit of that field, " +
	"so edit the field back to the hub's value yourself"

// routesFor lists the edit that sends each field in fields.
func routesFor(fields []string) string {
	seen := map[string]bool{}
	var parts []string
	for _, f := range fields {
		if r, ok := localOnlyRoutes[f]; ok && !seen[f] {
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
