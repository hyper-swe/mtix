// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// An env that reaches the project is reviewed like any program that does.
func TestClassifyProgram_EnvResolvingInsideProject_IsOtherWithPinFix(t *testing.T) {
	l := newPathLinkLayout(t, "env")
	h := Hook{Name: "wake", Exec: &ExecConfig{Command: []string{filepath.Join(l.linked, "env"), "sh", "scripts/run.sh"}}}
	review := classifyProgram(h, &hookInputs{root: l.root, program: 1, script: 2})
	require.Equal(t, ClassOther, review.Class)
	require.True(t, review.Needed)
	require.Equal(t, filepath.Join(l.root, "bin", "env"), review.Program)
	require.Contains(t, review.Fix, "resolves inside the project")
	require.NotContains(t, review.Fix, "trust --program")
}

func TestClassifyProgram_EnvOutsideProject_KeepsItsClass(t *testing.T) {
	l := newPathLinkLayout(t, "env")
	h := Hook{Name: "wake", Exec: &ExecConfig{Command: []string{filepath.Join(l.plain, "env"), "sh", "scripts/run.sh"}}}
	in := &hookInputs{root: l.root, program: 1, script: 2}
	review := classifyProgram(h, in)
	require.NotContains(t, review.Fix, "resolves inside the project")
}

// A strict-mode entry for a program that resolves inside the project says to
// pin it, never to approve it: the approval would be refused.
func TestStrictStatus_ProgramResolvingInsideProject_FixIsPinNotApproval(t *testing.T) {
	f := newFixture(t, "[{A}/bin/env, sh, scripts/run.sh]", fixtureOptions{aliased: true, extra: map[string]string{"bin/env": "#!/bin/sh\n"}})
	require.NoError(t, f.approvals.SetStrict(true, true))
	cfg, _ := Load(f.mtixDir)
	st := f.approvals.StrictStatus(cfg)
	require.Len(t, st.Programs, 1)
	require.False(t, st.Programs[0].Approved)
	require.Contains(t, st.Programs[0].Detail, "inside the project")
	require.NotContains(t, st.Programs[0].Fix, "trust --program")
	require.NotContains(t, st.Fix, "trust --program")
	require.Contains(t, st.Programs[0].Fix, "resolves inside the project")
	_, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.Error(t, err, "the program approval the old fix named is refused")
}

// The decision is the one launch makes, by file identity: a link target
// spelled with different letter case is still the project.
func TestKnownProgramPin_CaseVariantLinkTargetIsInsideTheProject(t *testing.T) {
	l := newPathLinkLayout(t, "python3")
	variant := strings.ToUpper(filepath.Join(l.root, "bin"))
	if _, err := os.Stat(variant); err != nil {
		t.Skip("case-sensitive volume: an upper-cased spelling names a different directory")
	}
	linked := filepath.Join(t.TempDir(), "casebin")
	require.NoError(t, os.Symlink(variant, linked))
	known := func(p string) bool { return reviewBaseName(p) == "python3" }
	for _, program := range []string{filepath.Join(linked, "python3"), "python3"} {
		_, ok := knownProgramPinIn(l.root, []string{linked}, program, known)
		require.False(t, ok, program)
	}
}

// A program whose location cannot be decided is not trusted by its name:
// launch fails closed on an incomplete answer, and so does the review.
func TestKnownProgramPin_UndecidableLocation_IsNotKnown(t *testing.T) {
	l := newPathLinkLayout(t, "python3")
	head := projectLinkChain(t, maxLinkExpansions+6, filepath.Join(l.root, "bin", "python3"))
	require.NoError(t, os.Remove(filepath.Join(l.plain, "python3")))
	require.NoError(t, os.Symlink(head, filepath.Join(l.plain, "python3")))
	known := func(p string) bool { return reviewBaseName(p) == "python3" }
	_, ok := knownProgramPinIn(l.root, []string{l.plain}, filepath.Join(l.plain, "python3"), known)
	require.False(t, ok)
	h := Hook{Name: "wake", Exec: &ExecConfig{Command: []string{filepath.Join(l.plain, "python3"), "scripts/run.sh"}}}
	review := classifyProgram(h, &hookInputs{root: l.root, program: 0, script: 1})
	require.Contains(t, review.Fix, "resolves inside the project or its location cannot be decided")
}

// The env judgement comes before the pinned-script and interpreter rules: an
// env that reaches the project is reviewed even when the program it runs is a
// pinned project script.
func TestClassifyProgram_EnvInsideProjectRunningPinnedScript_IsStillOther(t *testing.T) {
	l := newPathLinkLayout(t, "env")
	h := Hook{Name: "wake", Exec: &ExecConfig{Command: []string{filepath.Join(l.linked, "env"), "sh", "scripts/run.sh"}}}
	in := &hookInputs{root: l.root, program: 1, script: 2, scripts: map[int]scriptInput{2: {path: "scripts/run.sh"}}}
	review := classifyProgram(h, in)
	require.Equal(t, ClassOther, review.Class)
	require.True(t, review.Needed)
	require.Contains(t, review.Fix, "resolves inside the project")
	require.NotContains(t, review.Fix, "trust --program")
}
