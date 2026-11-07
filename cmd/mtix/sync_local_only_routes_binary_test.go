//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// MTIX-112.17.16.16 round 3, principle C: every route mtix doctor prints for
// a local-only imported field is run on the real binary and must write an
// event that clears the entry. A route that does not is not printed.

type routeCase struct {
	field string
	state string // for status: the state the task is in, as the doctor names it
	setup string // SQL run on the task before the entry is recorded
	value string // what the printed <current...> placeholder stands for
}

func routeCases() []routeCase {
	return []routeCase{
		{"title", "", "", "Routed title"}, {"description", "", "", "Routed"}, {"prompt", "", "", "Routed"},
		{"acceptance", "", "", "Routed"}, {"labels", "", "", "routed"}, {"priority", "", "", "2"},
		{"assignee", "", "", "route-op"}, {"issue_type", "", "", "bug"}, {"annotations", "", "", "routed note"},
		{"status", "done", "UPDATE nodes SET status = 'done', progress = 1, closed_at = '2026-01-01T00:00:00Z' WHERE id = '{id}'", ""},
		{"status", "cancelled", "UPDATE nodes SET status = 'cancelled', closed_at = '2026-01-01T00:00:00Z' WHERE id = '{id}'", ""},
		{"status", "in_progress", "UPDATE nodes SET status = 'in_progress', assignee = 'route-op', agent_state = 'working' WHERE id = '{id}'", ""},
		{"status", "open", "", ""},
		{"status", "deferred", "UPDATE nodes SET status = 'deferred' WHERE id = '{id}'", ""},
	}
}

var backtickCommand = regexp.MustCompile("`([^`]+)`")

// printedSteps reads the commands the doctor printed for a field (and, for
// status, a state) out of the check's own Fix text.
func printedSteps(t *testing.T, fix string, c routeCase) []string {
	t.Helper()
	_, tail, ok := strings.Cut(fix, "Routes by field: ")
	require.True(t, ok, "the fix carries the routes: %s", fix)
	var clause string
	for _, part := range strings.Split(tail, "; ") {
		if rest, found := strings.CutPrefix(part, c.field+": "); found {
			clause = rest
		}
	}
	require.NotEmpty(t, clause, "no route printed for %s", c.field)
	if c.field == "status" {
		var seg string
		for _, part := range strings.Split(clause, " | ") {
			if i := strings.Index(part, c.state+": "); i >= 0 {
				seg = part[i+len(c.state)+2:]
			}
		}
		require.NotEmpty(t, seg, "no status route printed for %s", c.state)
		clause = seg
	}
	var cmds []string
	for _, m := range backtickCommand.FindAllStringSubmatch(clause, -1) {
		cmds = append(cmds, m[1])
	}
	require.NotEmpty(t, cmds, "the printed route has no command: %s", clause)
	return cmds
}

var routePlaceholder = regexp.MustCompile("<[^>]+>")

// printedArgs substitutes the placeholders of one printed command and splits
// it as a shell would for double quotes; the leading "mtix" is dropped.
func printedArgs(t *testing.T, cmd, id string, c routeCase) []string {
	t.Helper()
	cmd = routePlaceholder.ReplaceAllStringFunc(cmd, func(p string) string {
		switch p {
		case "<id>":
			return id
		case "<agent>", "<assignee>":
			return "route-op"
		case "<why>":
			return "routed"
		}
		return c.value
	})
	var args []string
	var cur strings.Builder
	quoted, started := false, false
	for _, r := range cmd {
		switch {
		case r == '"':
			quoted, started = !quoted, true
		case r == ' ' && !quoted:
			if started {
				args = append(args, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if started {
		args = append(args, cur.String())
	}
	require.Equal(t, "mtix", args[0], cmd)
	return args[1:]
}

func TestLocalOnlyRoutes_RealBinary_EachPrintedRouteWritesAnEventAndClearsTheEntry(t *testing.T) {
	binary := buildQuarantineRecoveryCLI(t)
	initTestApp(t)
	operator := operatorTestHome(t)
	f := quarantineRecoveryCLI{binary: binary, project: filepath.Dir(app.mtixDir)}
	f.env = append(hostguard.Environ(), "MTIX_AUTHOR_ID=route-op", "XDG_CONFIG_HOME="+operator, "APPDATA="+operator)
	for i, c := range routeCases() {
		id := fmt.Sprintf("TEST-%d", i+1)
		t.Run(fmt.Sprintf("%s/%s/%d", c.field, c.state, i), func(t *testing.T) {
			require.NoError(t, runCreate(fmt.Sprintf("route task %d", i), "", "", 3, "", "", "", "", ""))
			if c.setup != "" {
				_, err := app.store.WriteDB().Exec(strings.ReplaceAll(c.setup, "{id}", id))
				require.NoError(t, err)
			}
			require.NoError(t, app.store.RecordLocalOnlyImports(t.Context(), []sqlite.LocalOnlyImport{{ID: id, Fields: []string{c.field}}}))
			check := localOnlyCheck(t)
			require.Contains(t, check.LocalOnly, id, "the entry is live before the route")

			for _, printed := range printedSteps(t, check.Fix, c) { // exactly what the doctor printed
				out := f.run(t, printedArgs(t, printed, id, c)...)
				require.Zero(t, out.exitCode, "%s: %s%s", printed, out.stdout, out.stderr)
			}

			require.NotContains(t, localOnlyCheck(t).LocalOnly, id, "the route cleared the entry")
		})
	}
}

// TestLocalOnlyRoutes_StatusTable_NamesEveryStateAndNoRouteOnes: the doctor
// names every status; blocked and invalidated carry no command.
func TestLocalOnlyRoutes_StatusTable_NamesEveryStateAndNoRouteOnes(t *testing.T) {
	text := routesFor([]string{"status", "annotations"})
	for _, st := range []string{"done", "cancelled", "in_progress", "open", "deferred", "blocked", "invalidated"} {
		require.Contains(t, text, st+": ")
	}
	require.Contains(t, text, "invalidated: no route, it stays on this machine")
	require.Contains(t, text, "`mtix cancel <id> --reason \"<why>\"`")
	require.Contains(t, text, "`mtix unclaim <id> --reason \"<why>\" --agent <assignee>`")
	require.Contains(t, text, "adds a NEW comment, the original stays local")
}

func TestLocalOnlyRoutes_RealBinary_DependencyRemoveThenAddClearsTheEntry(t *testing.T) {
	binary := buildQuarantineRecoveryCLI(t)
	initTestApp(t)
	operator := operatorTestHome(t)
	f := quarantineRecoveryCLI{binary: binary, project: filepath.Dir(app.mtixDir)}
	f.env = append(hostguard.Environ(), "MTIX_AUTHOR_ID=route-op", "XDG_CONFIG_HOME="+operator, "APPDATA="+operator)
	require.NoError(t, runCreate("blocker", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("dependent", "", "", 3, "", "", "", "", ""))
	out := f.run(t, "dep", "add", "TEST-1", "TEST-2")
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	require.NoError(t, app.store.RecordLocalOnlyImports(t.Context(), []sqlite.LocalOnlyImport{{ID: "TEST-1", Fields: []string{"dependencies"}}}))
	require.Contains(t, localOnlyCheck(t).LocalOnly, "TEST-1")
	require.Contains(t, routesFor([]string{"dependencies"}), "`mtix dep remove <from-id> <to-id>`, then `mtix dep add")

	for _, step := range [][]string{{"dep", "remove", "TEST-1", "TEST-2"}, {"dep", "add", "TEST-1", "TEST-2"}} {
		out = f.run(t, step...)
		require.Zero(t, out.exitCode, "%v: %s%s", step, out.stdout, out.stderr)
	}

	require.NotContains(t, localOnlyCheck(t).LocalOnly, "TEST-1")
}
