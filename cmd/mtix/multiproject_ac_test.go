// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// FR-MULTI-PROJECT acceptance tests exercised against the REAL integrated CLI
// run* paths (no mocks). The configured primary in initTestApp is "TEST"; the
// second project is the multi-hyphen "MTIX-DEV-OPS" so these tests also cover
// the AC-4 sharp edge at the CLI surface (create/list/orphans/projects).
//
//   AC-1  cross-project create, child inheritance, parent mismatch error
//   AC-2  list-style scope (orphans, in addition to list/search in
//         projects_scope_test.go): default=primary, --project, --all-projects
//   AC-3  `mtix projects` lists both with counts, primary marked
//   AC-7  single-project DB behaves identically to pre-feature (regression)
//
// Helpers captureStdout / extractNodeIDs / projectsOf live in
// projects_scope_test.go (same package). projectsOf splits at the first '-' so
// it is NOT prefix-aware for multi-hyphen ids; these tests assert on the
// node.project field via nodeProjectsOf instead.
package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

const mpSecondProject = "MTIX-DEV-OPS"

// nodeProjectsOf returns the set of node.project values in a list-style JSON
// payload — prefix-aware, unlike projectsOf, so it is correct for multi-hyphen
// project ids such as MTIX-DEV-OPS-1.
func nodeProjectsOf(t *testing.T, out string) map[string]int {
	t.Helper()
	var payload struct {
		Nodes []struct {
			Project string `json:"project"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	counts := map[string]int{}
	for _, n := range payload.Nodes {
		counts[n.Project]++
	}
	return counts
}

// ----------------------------------------------------------------------------
// AC-1: create roots in two projects via the real create path; children inherit
// the parent's project; --project mismatching a parent errors.
// ----------------------------------------------------------------------------

func TestMultiProject_AC1_CreateRootsInheritAndMismatch(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()

	// Root in the primary (TEST) — default command, project already known after
	// the first --yes seed, so no prompt. Use --yes for the very first create.
	require.NoError(t, runCreateWithProject(
		"primary root", "", "", 3, "", "", "", "", "", "TEST", true))

	// Root in the multi-hyphen second project via the interactive confirm ("y").
	withCreateInput(t, "y\n")
	require.NoError(t, runCreateWithProject(
		"ops root", "", "", 3, "", "", "", "", "", mpSecondProject, false))

	opsRoot, err := app.nodeSvc.GetNode(ctx, "MTIX-DEV-OPS-1")
	require.NoError(t, err)
	assert.Equal(t, mpSecondProject, opsRoot.Project)

	// Child with no --project inherits the parent's (multi-hyphen) project.
	require.NoError(t, runCreateWithProject(
		"ops child", "MTIX-DEV-OPS-1", "", 3, "", "", "", "", "", "", true))
	child, err := app.nodeSvc.GetNode(ctx, "MTIX-DEV-OPS-1.1")
	require.NoError(t, err)
	assert.Equal(t, mpSecondProject, child.Project,
		"child inherits MTIX-DEV-OPS, not the TEST primary")

	// --project on a child that mismatches the parent's project is an error,
	// and nothing is created.
	err = runCreateWithProject(
		"bad child", "MTIX-DEV-OPS-1", "", 3, "", "", "", "", "", "TEST", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TEST")
	assert.Contains(t, err.Error(), mpSecondProject)
}

// ----------------------------------------------------------------------------
// AC-2: orphans (a list-style command beyond list/search) scopes to the primary
// by default, to a named project with --project, and spans all with
// --all-projects — never leaking nodes across prefixes.
// ----------------------------------------------------------------------------

// seedRootsForScope creates 2 TEST roots and 1 MTIX-DEV-OPS root via the real
// create path (all are orphans: no parent).
func seedRootsForScope(t *testing.T) {
	t.Helper()
	require.NoError(t, runCreateWithProject("t1", "", "", 3, "", "", "", "", "", "TEST", true))
	require.NoError(t, runCreateWithProject("t2", "", "", 3, "", "", "", "", "", "TEST", true))
	require.NoError(t, runCreateWithProject("ops1", "", "", 3, "", "", "", "", "", mpSecondProject, true))
}

func TestMultiProject_AC2_OrphansDefaultPrimaryOnly(t *testing.T) {
	initTestApp(t)
	seedRootsForScope(t)
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })

	out := captureStdout(t, func() { require.NoError(t, runOrphans("", false)) })
	assert.Equal(t, map[string]int{"TEST": 2}, nodeProjectsOf(t, out),
		"default scope leaks no MTIX-DEV-OPS node into the primary view")
}

func TestMultiProject_AC2_OrphansProjectFlag(t *testing.T) {
	initTestApp(t)
	seedRootsForScope(t)
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })

	out := captureStdout(t, func() { require.NoError(t, runOrphans(mpSecondProject, false)) })
	assert.Equal(t, map[string]int{mpSecondProject: 1}, nodeProjectsOf(t, out),
		"--project scopes to exactly the multi-hyphen project")
}

func TestMultiProject_AC2_OrphansAllProjects(t *testing.T) {
	initTestApp(t)
	seedRootsForScope(t)
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })

	out := captureStdout(t, func() { require.NoError(t, runOrphans("", true)) })
	assert.Equal(t, map[string]int{"TEST": 2, mpSecondProject: 1}, nodeProjectsOf(t, out))
}

func TestMultiProject_AC2_OrphansProjectAndAllConflict(t *testing.T) {
	initTestApp(t)
	err := runOrphans(mpSecondProject, true)
	require.Error(t, err, "--project and --all-projects are mutually exclusive")
}

// ----------------------------------------------------------------------------
// AC-3: `mtix projects` lists both projects with correct counts and the primary
// marked. Counts include children (live nodes), so seed a child too.
// ----------------------------------------------------------------------------

func TestMultiProject_AC3_ProjectsListsBothWithCounts(t *testing.T) {
	initTestApp(t)
	// 2 TEST nodes (root + child) and 1 MTIX-DEV-OPS root.
	require.NoError(t, runCreateWithProject("t root", "", "", 3, "", "", "", "", "", "TEST", true))
	require.NoError(t, runCreateWithProject("t child", "TEST-1", "", 3, "", "", "", "", "", "", true))
	require.NoError(t, runCreateWithProject("ops root", "", "", 3, "", "", "", "", "", mpSecondProject, true))

	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })

	out := captureStdout(t, func() { require.NoError(t, runProjects()) })
	var payload struct {
		Projects []struct {
			Prefix    string `json:"prefix"`
			Count     int    `json:"count"`
			IsPrimary bool   `json:"is_primary"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))

	counts := map[string]int{}
	primary := map[string]bool{}
	for _, p := range payload.Projects {
		counts[p.Prefix] = p.Count
		primary[p.Prefix] = p.IsPrimary
	}
	assert.Equal(t, 2, counts["TEST"])
	assert.Equal(t, 1, counts[mpSecondProject])
	assert.True(t, primary["TEST"], "TEST is the configured primary")
	assert.False(t, primary[mpSecondProject], "MTIX-DEV-OPS is not primary")
}

// ----------------------------------------------------------------------------
// AC-7 (regression): a single-project DB behaves identically to pre-feature —
// no --project needed, no prompts once the project exists, and `mtix projects`
// shows exactly one project.
// ----------------------------------------------------------------------------

func TestMultiProject_AC7_SingleProjectNoFlagsNoPrompts(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()

	// Establish the single (primary) project.
	require.NoError(t, runCreateWithProject("first", "", "", 3, "", "", "", "", "", "TEST", true))

	// Subsequent default creates take NO --project and would ABORT if a prompt
	// fired (the reader answers "n"). Because TEST is the only/known project, no
	// prompt appears — identical to pre-feature behavior.
	withCreateInput(t, "n\n")
	require.NoError(t, runCreateWithProject("second", "", "", 3, "", "", "", "", "", "", false))
	require.NoError(t, runCreateWithProject("child", "TEST-1", "", 3, "", "", "", "", "", "", false))

	_, err := app.nodeSvc.GetNode(ctx, "TEST-2")
	require.NoError(t, err)

	// list with no flag returns every node and only the one project.
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })
	out := captureStdout(t, func() {
		require.NoError(t, runList("", "", "", "", "", "", "", 0, false, 50, "", false))
	})
	got := nodeProjectsOf(t, out)
	assert.Equal(t, map[string]int{"TEST": 3}, got, "single-project list shows only TEST")

	// projects shows exactly one project, marked primary.
	out = captureStdout(t, func() { require.NoError(t, runProjects()) })
	var payload struct {
		Projects []struct {
			Prefix    string `json:"prefix"`
			IsPrimary bool   `json:"is_primary"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.Len(t, payload.Projects, 1, "exactly one project in a single-project DB")
	assert.Equal(t, "TEST", payload.Projects[0].Prefix)
	assert.True(t, payload.Projects[0].IsPrimary)
}

// TestMultiProject_AC7_PrimaryFirstCreate_PromptIsKnownEdge is the REGRESSION
// GUARD for MTIX-40: the configured primary is always treated as a known project
// by the new-project guardrail, so the very first default `mtix create` on a
// brand-new EMPTY database creates the primary root WITHOUT a "Create new
// project?" prompt — identical to pre-feature behavior (AC-7). The guardrail
// still fires only for a genuinely new, non-primary prefix.
func TestMultiProject_AC7_PrimaryFirstCreate_PromptIsKnownEdge(t *testing.T) {
	initTestApp(t)

	// Empty DB, default command (no --project, yes=false). Even with "n" queued,
	// the primary is exempt from the guardrail, so the create succeeds unprompted.
	withCreateInput(t, "n\n")
	require.NoError(t, runCreateWithProject("first", "", "", 3, "", "", "", "", "", "", false),
		"MTIX-40 fixed: the primary first-create on an empty DB must not prompt")
	_, getErr := app.nodeSvc.GetNode(context.Background(), "TEST-1")
	require.NoError(t, getErr)
}

// compile-time anchor that the service create request shape used by the seeders
// above stays stable (guards against silent signature drift).
var _ = service.CreateNodeRequest{}
