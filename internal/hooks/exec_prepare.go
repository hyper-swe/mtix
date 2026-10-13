// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// execCopiesDir names the private directory, under the operator state
// directory, that holds verified copies of approved project scripts.
const execCopiesDir = "exec-copies"

// ExecPlan is a prepared exec launch: an absolute argv whose project scripts
// are verified private copies, run from the project root (FR-19).
type ExecPlan struct {
	Argv []string
	Dir  string
	// ScriptPath is the original path of the first project script, exposed to
	// the command as MTIX_HOOK_SCRIPT_PATH; empty if the command names none.
	ScriptPath string
	// Resolved maps each bare command name to the absolute path it resolved to.
	Resolved map[string]string
}

// Audit renders the resolved bare names for the firing log, or "".
func (p *ExecPlan) Audit() string {
	parts := make([]string, 0, len(p.Resolved))
	for name, path := range p.Resolved {
		parts = append(parts, name+"="+path)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// PrepareExec reads the hook's stanza and project scripts once, requires the
// digest of exactly those bytes to match this host's approval, and stages each
// script as a private copy. Anything else returns an error and starts nothing.
func (a *Approvals) PrepareExec(h Hook) (*ExecPlan, error) {
	if h.Exec == nil || len(h.Exec.Command) == 0 {
		return nil, fmt.Errorf("exec launch: hook %q has no command", h.Name)
	}
	in, root, err := a.approvedInputs(h)
	if err != nil {
		return nil, err
	}
	if err = a.state.Ensure(); err != nil {
		return nil, fmt.Errorf("exec launch: %w", err)
	}
	opDir, err := a.state.Path()
	if err != nil {
		return nil, fmt.Errorf("exec launch: %w", err)
	}
	return buildPlan(h, in, root, filepath.Join(opDir, execCopiesDir))
}

// approvedInputs reads the hook's inputs once and requires their digest to
// match this host's approval.
func (a *Approvals) approvedInputs(h Hook) (*hookInputs, string, error) {
	logical, root, err := projectRoots(a.dir)
	if err != nil {
		return nil, "", err
	}
	project, err := projectIdentity(a.dir)
	if err != nil {
		return nil, "", err
	}
	in, err := collectHookInputs(h, logical, root)
	if err != nil {
		return nil, "", err
	}
	trusted, detail, err := a.readDecision(project, h.Name, in.digest)
	if err != nil {
		return nil, "", fmt.Errorf("exec launch: %w", err)
	}
	if !trusted {
		return nil, "", fmt.Errorf("exec launch refused for hook %q: %s", h.Name, detail)
	}
	return in, root, nil
}

func buildPlan(h Hook, in *hookInputs, root, copies string) (*ExecPlan, error) {
	plan := &ExecPlan{Dir: root, Resolved: map[string]string{}}
	for i, arg := range h.Exec.Command {
		if script, ok := in.scripts[i]; ok {
			staged, err := stageScript(copies, script)
			if err != nil {
				return nil, err
			}
			if plan.ScriptPath == "" {
				plan.ScriptPath = script.path
			}
			plan.Argv = append(plan.Argv, staged)
			continue
		}
		resolved, err := launchOperand(h.Name, root, i, arg)
		if err != nil {
			return nil, err
		}
		if resolved != arg {
			plan.Resolved[arg] = resolved
		}
		plan.Argv = append(plan.Argv, resolved)
	}
	return plan, nil
}

// launchOperand handles an operand that is not a staged copy: the program
// (index 0) is made absolute, and an unstaged project path in the program or
// script-operand position is refused.
func launchOperand(hook, root string, index int, arg string) (string, error) {
	if index <= 1 && unstagedProjectPath(root, arg) {
		return "", fmt.Errorf("exec launch refused for hook %q: %q is a project path with no regular file at approval; create it and approve the hook again", hook, arg)
	}
	if index != 0 {
		return arg, nil
	}
	resolved, err := resolveLauncher(root, arg)
	if err != nil {
		return "", err
	}
	if insideRoot(root, resolved) {
		return "", fmt.Errorf("exec launch refused for hook %q: %q resolves to %q inside the project; name the script by its project path", hook, arg, resolved)
	}
	return resolved, nil
}

// unstagedProjectPath reports a path-like operand (no whitespace, absolute or
// with a separator) that lies inside the project but was not staged as a copy:
// running it by path would execute bytes that were never approved.
func unstagedProjectPath(root, arg string) bool {
	if strings.ContainsAny(arg, " \t\n") || (!filepath.IsAbs(arg) && !strings.ContainsRune(arg, filepath.Separator)) {
		return false
	}
	_, inside, err := projectScriptLocation(root, root, arg)
	return err == nil && inside
}

// insideRoot reports whether the symlink-resolved path lies in the project.
func insideRoot(root, path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, resolved)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveLauncher makes the program absolute: a bare name through PATH, a
// relative path against the project root.
func resolveLauncher(root, name string) (string, error) {
	switch {
	case filepath.IsAbs(name):
		return name, nil
	case filepath.Base(name) == name:
		path, err := exec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("exec launch: resolve %q: %w", name, err)
		}
		return filepath.Abs(path)
	default:
		return filepath.Join(root, name), nil
	}
}

// stageScript writes the script bytes already read into a content-named 0700
// directory and returns the copy's path after re-reading it.
func stageScript(base string, s scriptInput) (string, error) {
	sum := sha256.Sum256(s.content)
	perm, kind := os.FileMode(0o600), "-r"
	if s.mode.Perm()&0o111 != 0 {
		perm, kind = 0o700, "-x"
	}
	// The mode is not part of the approval digest, so each mode gets its own
	// content-named directory and a chmod on the original reaches the copy.
	dir := filepath.Join(base, hex.EncodeToString(sum[:])+kind)
	for _, d := range []string{base, dir} {
		if err := privateDir(d); err != nil {
			return "", err
		}
	}
	target := filepath.Join(dir, filepath.Base(s.path))
	if sameBytes(target, s.content) {
		return target, nil
	}
	tmp, err := os.CreateTemp(dir, ".stage-*")
	if err != nil {
		return "", fmt.Errorf("stage script: %w", err)
	}
	_, werr := tmp.Write(s.content)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Chmod(tmp.Name(), perm) != nil || os.Rename(tmp.Name(), target) != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("stage script: copy for %q failed", filepath.Base(s.path))
	}
	if stagedHook != nil {
		stagedHook(target)
	}
	if !sameBytes(target, s.content) {
		return "", fmt.Errorf("stage script: copy for %q does not match the approved bytes", filepath.Base(s.path))
	}
	return target, nil
}

// privateDir creates dir with mode 0700 if absent and refuses a link or any
// other kind of entry.
func privateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("stage script: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("stage script: %q is not a directory", filepath.Base(dir))
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("stage script: %w", err)
	}
	return nil
}

// stagedHook runs after a copy is renamed into place; tests use it to corrupt
// the copy and prove the re-read check.
var stagedHook func(path string)

func sameBytes(path string, want []byte) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	got, err := os.ReadFile(path) //nolint:gosec // reads back the private verified copy
	return err == nil && bytes.Equal(got, want)
}
