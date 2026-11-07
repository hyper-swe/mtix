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

// statusRoute is what to run to send a task's status, by the state it has
// now: the steps move it through another status and back so a transition
// event carries the current one. Steps are printed in backticks and run as
// printed after substituting <id>, <agent> and <why>; a state with no steps
// has no route and the note says why.
type statusRoute struct {
	state string
	steps []string
	note  string
}

// statusRoutes is the table the doctor prints and the real-binary test parses
// and runs (TestLocalOnlyRoutes_RealBinary_*).
func statusRoutes() []statusRoute {
	return []statusRoute{
		{"done", []string{"mtix reopen <id>", "mtix claim <id> --agent <agent>", "mtix done <id>"}, ""},
		{"cancelled", []string{"mtix reopen <id>", `mtix cancel <id> --reason "<why>"`}, ""},
		{"in_progress", []string{`mtix unclaim <id> --reason "<why>" --agent <assignee>`, "mtix claim <id> --agent <assignee>"}, ""},
		{"open", []string{"mtix claim <id> --agent <agent>", `mtix unclaim <id> --reason "<why>" --agent <agent>`}, ""},
		{"deferred", []string{"mtix reopen <id>", "mtix defer <id>"}, "the wake time is not sent"},
		{"blocked", nil, "no route, change its dependencies"},
		{"invalidated", nil, "no route, it stays on this machine"},
	}
}

// statusRouteText prints statusRoutes as one clause per state, separated by " | ".
func statusRouteText() string {
	parts := make([]string, 0, len(statusRoutes()))
	for _, r := range statusRoutes() {
		cmds := make([]string, len(r.steps))
		for i, c := range r.steps {
			cmds[i] = "`" + c + "`"
		}
		text := r.state + ": " + strings.Join(cmds, ", then ")
		switch {
		case len(cmds) == 0:
			text = r.state + ": " + r.note
		case r.note != "":
			text += " (" + r.note + ")"
		}
		parts = append(parts, text)
	}
	return "move the task through another status and back, by the status it has now: " + strings.Join(parts, " | ")
}

// localOnlyRoutes is what to run to send an imported value, by field: the
// edit that journals the field's current value. Every command is printed in
// backticks and run as printed by TestLocalOnlyRoutes_RealBinary, which reads
// the doctor's own text. A field with no route (wake time, agent state, a
// deletion) is not recorded: it is named in the import notice as staying on
// this machine. The task drops off the list once every listed field has an
// event after the import.
func localOnlyRoutes() map[string]string {
	return map[string]string{
		"title": "`mtix update <id> --title \"<current title>\"`", "description": "`mtix update <id> --description \"<current>\"`",
		"prompt": "`mtix update <id> --prompt \"<current>\"`", "acceptance": "`mtix update <id> --acceptance \"<current>\"`",
		"labels": "`mtix update <id> --labels \"<current>\"`", "priority": "`mtix update <id> --priority <current>`",
		"assignee": "`mtix update <id> --assignee \"<current>\"`", "issue_type": "`mtix update <id> --type <current>`",
		"status":      statusRouteText(),
		"annotations": "`mtix comment <id> \"<text>\"` (adds a NEW comment, the original stays local)",
		"dependencies": "for each dependency: `mtix dep remove <from-id> <to-id>`, then `mtix dep add <from-id> <to-id>` " +
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
