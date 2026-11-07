//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"path/filepath"
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
	setup string     // SQL run on the task before the entry is recorded
	steps [][]string // the printed commands, with {id} for the task
}

func routeCases() []routeCase {
	return []routeCase{
		{"title", "", [][]string{{"update", "{id}", "--title", "Routed title"}}},
		{"description", "", [][]string{{"update", "{id}", "--description", "Routed"}}},
		{"prompt", "", [][]string{{"update", "{id}", "--prompt", "Routed"}}},
		{"acceptance", "", [][]string{{"update", "{id}", "--acceptance", "Routed"}}},
		{"labels", "", [][]string{{"update", "{id}", "--labels", "routed"}}},
		{"priority", "", [][]string{{"update", "{id}", "--priority", "2"}}},
		{"assignee", "", [][]string{{"update", "{id}", "--assignee", "route-op"}}},
		{"issue_type", "", [][]string{{"update", "{id}", "--type", "bug"}}},
		{"annotations", "", [][]string{{"comment", "{id}", "routed note"}}},
		{"status", "UPDATE nodes SET status = 'done', progress = 1, closed_at = '2026-01-01T00:00:00Z' WHERE id = '{id}'",
			[][]string{{"reopen", "{id}"}, {"claim", "{id}", "--agent", "route-op"}, {"done", "{id}"}}},
		{"status", "UPDATE nodes SET status = 'cancelled', closed_at = '2026-01-01T00:00:00Z' WHERE id = '{id}'",
			[][]string{{"reopen", "{id}"}, {"cancel", "{id}", "--reason", "routed"}}},
		{"status", "UPDATE nodes SET status = 'in_progress', assignee = 'route-op', agent_state = 'working' WHERE id = '{id}'",
			[][]string{{"unclaim", "{id}", "--reason", "routed", "--agent", "route-op"}, {"claim", "{id}", "--agent", "route-op"}}},
		{"status", "",
			[][]string{{"claim", "{id}", "--agent", "route-op"}, {"unclaim", "{id}", "--reason", "routed", "--agent", "route-op"}}},
		{"status", "UPDATE nodes SET status = 'deferred' WHERE id = '{id}'",
			[][]string{{"reopen", "{id}"}, {"defer", "{id}"}}},
	}
}

func substID(args []string, id string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a
		if a == "{id}" {
			out[i] = id
		}
	}
	return out
}

func TestLocalOnlyRoutes_RealBinary_EachPrintedRouteWritesAnEventAndClearsTheEntry(t *testing.T) {
	binary := buildQuarantineRecoveryCLI(t)
	initTestApp(t)
	operator := operatorTestHome(t)
	f := quarantineRecoveryCLI{binary: binary, project: filepath.Dir(app.mtixDir)}
	f.env = append(hostguard.Environ(), "MTIX_AUTHOR_ID=route-op", "XDG_CONFIG_HOME="+operator, "APPDATA="+operator)
	for i, c := range routeCases() {
		id := fmt.Sprintf("TEST-%d", i+1)
		t.Run(fmt.Sprintf("%s/%d", c.field, i), func(t *testing.T) {
			require.NoError(t, runCreate(fmt.Sprintf("route task %d", i), "", "", 3, "", "", "", "", ""))
			if c.setup != "" {
				_, err := app.store.WriteDB().Exec(strings.ReplaceAll(c.setup, "{id}", id))
				require.NoError(t, err)
			}
			require.NoError(t, app.store.RecordLocalOnlyImports(t.Context(), []sqlite.LocalOnlyImport{{ID: id, Fields: []string{c.field}}}))
			require.Contains(t, localOnlyCheck(t).LocalOnly, id, "the entry is live before the route")
			require.Contains(t, routesFor([]string{c.field}), c.steps[0][0], "the doctor prints this route")

			for _, step := range c.steps {
				out := f.run(t, substID(step, id)...)
				require.Zero(t, out.exitCode, "%v: %s%s", step, out.stdout, out.stderr)
			}

			require.NotContains(t, localOnlyCheck(t).LocalOnly, id, "the route cleared the entry")
		})
	}
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
	require.Contains(t, routesFor([]string{"dependencies"}), "mtix dep remove <from-id> <to-id>, then mtix dep add")

	for _, step := range [][]string{{"dep", "remove", "TEST-1", "TEST-2"}, {"dep", "add", "TEST-1", "TEST-2"}} {
		out = f.run(t, step...)
		require.Zero(t, out.exitCode, "%v: %s%s", step, out.stdout, out.stderr)
	}

	require.NotContains(t, localOnlyCheck(t).LocalOnly, "TEST-1")
}
