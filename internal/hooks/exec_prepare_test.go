// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/testutil"
)

const prepareScript = "#!/bin/sh\npwd -P > out.cwd\nprintf %s \"$0\" > out.zero\nprintf %s \"$MTIX_HOOK_SCRIPT_PATH\" > out.orig\ncat > out.stdin\n"

type prepareFixture struct {
	root, mtixDir, script string
	state                 *oplocal.State
	approvals             *Approvals
}

// newPrepareFixture builds an approved exec hook whose command is the given
// argv template ("{script}" expands to the project script's relative path).
func newPrepareFixture(t *testing.T, command string) prepareFixture {
	t.Helper()
	return newPrepareFixtureWith(t, command, nil)
}

// newPrepareFixtureWith also writes extra project files (path to content)
// before the hook is approved.
func newPrepareFixtureWith(t *testing.T, command string, extra map[string]string) prepareFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	mtixDir := filepath.Join(root, ".mtix")
	require.NoError(t, os.Mkdir(mtixDir, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "scripts"), 0o700))
	script := filepath.Join(root, "scripts", "run.sh")
	require.NoError(t, os.WriteFile(script, []byte(prepareScript), 0o700))
	for name, content := range extra {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o700))
	}
	yaml := "hooks:\n  - name: wake\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: " + command + "}\n"
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "hooks.yaml"), []byte(yaml), 0o600))
	state := testutil.OperatorState(t)
	approvals := NewApprovals(mtixDir, state, false)
	require.NoError(t, approvals.Approve("wake", true))
	return prepareFixture{root: root, mtixDir: mtixDir, script: script, state: state, approvals: approvals}
}

func (f prepareFixture) hook(t *testing.T) Hook {
	t.Helper()
	cfg, _ := Load(f.mtixDir)
	require.Len(t, cfg.Hooks, 1)
	return cfg.Hooks[0]
}

func readOut(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	require.NoError(t, err)
	return string(data)
}

func TestExecAdapter_LaunchUsesRootAndVerifiedCopy(t *testing.T) {
	f := newPrepareFixture(t, "[scripts/run.sh]")
	t.Chdir(t.TempDir()) // the dispatcher's own working directory is unrelated
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.NoError(t, NewExecAdapter().Deliver(context.Background(), Delivery{Hook: f.hook(t), EventJSON: []byte(`{}`), Exec: plan}))
	require.Eventually(t, func() bool { _, e := os.Stat(filepath.Join(f.root, "out.stdin")); return e == nil }, 10*time.Second, 20*time.Millisecond)

	require.Equal(t, f.root, strings.TrimSpace(readOut(t, f.root, "out.cwd")), "launch directory is the project root")
	require.Equal(t, f.script, readOut(t, f.root, "out.orig"), "original path is exposed")
	zero := readOut(t, f.root, "out.zero")
	require.NotEqual(t, f.script, zero, "argv0 is the verified copy")
	opDir, err := f.state.Path()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(zero, opDir+string(filepath.Separator)), "copy lives under the operator directory")
	info, err := os.Stat(filepath.Dir(zero))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	got, err := os.ReadFile(zero)
	require.NoError(t, err)
	require.Equal(t, prepareScript, string(got))
}

func TestExecAdapter_RefusesWithoutPreparedPlan(t *testing.T) {
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	marker := filepath.Join(t.TempDir(), "started")
	cmd := []string{sh, "-c", "touch " + marker}
	err = NewExecAdapter().Deliver(context.Background(), Delivery{Hook: Hook{Name: "h", Exec: &ExecConfig{Command: cmd}}})
	require.Error(t, err, "a delivery without a prepared launch plan starts nothing")
	time.Sleep(300 * time.Millisecond)
	_, statErr := os.Stat(marker)
	require.True(t, os.IsNotExist(statErr), "nothing was started")
}

func TestPrepareExec_ChangedInputStartsNothing(t *testing.T) {
	f := newPrepareFixture(t, "[scripts/run.sh]")
	require.NoError(t, os.WriteFile(f.script, []byte(prepareScript+"# changed\n"), 0o700))
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	require.Nil(t, plan)
}

func TestPrepareExec_RunsApprovedBytesAfterLaterChange(t *testing.T) {
	f := newPrepareFixture(t, "[scripts/run.sh]")
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.script, []byte("#!/bin/sh\ntouch changed.marker\n"), 0o700))
	require.NoError(t, NewExecAdapter().Deliver(context.Background(), Delivery{Hook: f.hook(t), EventJSON: []byte(`{}`), Exec: plan}))
	require.Eventually(t, func() bool { _, e := os.Stat(filepath.Join(f.root, "out.stdin")); return e == nil }, 10*time.Second, 20*time.Millisecond)
	_, statErr := os.Stat(filepath.Join(f.root, "changed.marker"))
	require.True(t, os.IsNotExist(statErr), "the approved bytes ran, not the replacement")
}

func TestPrepareExec_BareNameResolvedToAbsolute(t *testing.T) {
	f := newPrepareFixture(t, "[sh, scripts/run.sh]")
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	want, err := exec.LookPath("sh")
	require.NoError(t, err)
	require.Equal(t, want, plan.Argv[0])
	require.Equal(t, want, plan.Resolved["sh"], "resolution is recorded for the audit log")
	require.Equal(t, f.root, plan.Dir)
}

func TestApprovalDigest_IgnoresInterpreterBytes(t *testing.T) {
	interp := filepath.Join(t.TempDir(), "interp")
	require.NoError(t, os.WriteFile(interp, []byte("#!/bin/sh\nexec /bin/sh \"$@\"\n"), 0o700))
	f := newPrepareFixture(t, "["+interp+", scripts/run.sh]")
	require.NoError(t, os.WriteFile(interp, []byte("#!/bin/sh\n# upgraded\nexec /bin/sh \"$@\"\n"), 0o700))
	decisions, err := f.approvals.CheckConfig(Config{Hooks: []Hook{f.hook(t)}})
	require.NoError(t, err)
	require.True(t, decisions["wake"].Trusted, "interpreter upgrade needs no re-approval")
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
}

func TestPrepareExec_ModeChangeGetsMatchingCopy(t *testing.T) {
	f := newPrepareFixture(t, "[scripts/run.sh]")
	require.NoError(t, os.Chmod(f.script, 0o600))
	first, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.NoError(t, os.Chmod(f.script, 0o700))
	second, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	info, err := os.Stat(second.Argv[0])
	require.NoError(t, err)
	require.NotZero(t, info.Mode().Perm()&0o100, "the copy follows the original's executable bit")
	require.NotEqual(t, first.Argv[0], second.Argv[0])
	require.NoError(t, os.Chmod(f.script, 0o600))
	third, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	info, err = os.Stat(third.Argv[0])
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0o111, "removing the bit also reaches the copy")
}

func TestPrepareExec_PathTargetInsideProjectRefused(t *testing.T) {
	f := newPrepareFixture(t, "[tool]")
	bin := filepath.Join(f.root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tool"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	require.Nil(t, plan)
}

func TestPrepareExec_UnstagedProjectOperandRefused(t *testing.T) {
	for _, command := range []string{"[scripts/later.sh]", "[sh, scripts/later.sh]"} {
		t.Run(command, func(t *testing.T) {
			f := newPrepareFixture(t, command)
			plan, err := f.approvals.PrepareExec(f.hook(t))
			require.Error(t, err, "a project path with no file at approval is never run by path")
			require.Nil(t, plan)
		})
	}
}

func TestStageScript_ReplacesWrongOrLinkedTarget(t *testing.T) {
	content := []byte("#!/bin/sh\nexit 0\n")
	base := filepath.Join(t.TempDir(), "copies")
	script := scriptInput{path: "/p/run.sh", content: content, mode: 0o700}
	good, err := stager{}.stage(base, script)
	require.NoError(t, err)
	outside := filepath.Join(t.TempDir(), "same-bytes")
	require.NoError(t, os.WriteFile(outside, content, 0o600))
	for _, plant := range []func(){
		func() { require.NoError(t, os.WriteFile(good, []byte("wrong"), 0o700)) },
		func() { require.NoError(t, os.Symlink(outside, good)) },
	} {
		require.NoError(t, os.Remove(good))
		plant()
		got, err := stager{}.stage(base, script)
		require.NoError(t, err)
		info, err := os.Lstat(got)
		require.NoError(t, err)
		require.True(t, info.Mode().IsRegular(), "the target is a regular file, not a link")
		data, err := os.ReadFile(got)
		require.NoError(t, err)
		require.Equal(t, content, data)
	}
}

func TestStageScript_DetectsCopyChangedAfterWrite(t *testing.T) {
	st := stager{afterWrite: func(path string) { require.NoError(t, os.WriteFile(path, []byte("tampered"), 0o700)) }}
	_, err := st.stage(filepath.Join(t.TempDir(), "copies"), scriptInput{path: "/p/run.sh", content: []byte("#!/bin/sh\n"), mode: 0o700})
	require.Error(t, err)
}
