// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
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
	// Path is the fixed PATH of the child (FixedHookPath).
	Path string
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
	return buildPlan(h, in, root, filepath.Join(opDir, execCopiesDir), a.stager)
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

func buildPlan(h Hook, in *hookInputs, root, copies string, st stager) (*ExecPlan, error) {
	if err := checkOperands(h, in); err != nil {
		return nil, err
	}
	plan := &ExecPlan{Dir: root, Resolved: map[string]string{}, Path: FixedHookPath(root)}
	for i, arg := range h.Exec.Command {
		if script, ok := in.scripts[i]; ok {
			staged, err := st.stage(copies, script)
			if err != nil {
				return nil, err
			}
			if plan.ScriptPath == "" {
				plan.ScriptPath = script.path
			}
			plan.Argv = append(plan.Argv, script.prefix+staged)
			continue
		}
		resolved := arg
		if i == 0 || i == in.program {
			var err error
			if resolved, err = launchProgram(h.Name, in, arg); err != nil {
				return nil, err
			}
			if resolved != arg {
				plan.Resolved[arg] = resolved
			}
		}
		plan.Argv = append(plan.Argv, resolved)
	}
	return plan, nil
}

// launchProgram makes the program (index 0) absolute and refuses one that
// resolves into the project.
func launchProgram(hook string, in *hookInputs, arg string) (string, error) {
	if hasParentSegment(arg) {
		return "", fmt.Errorf("exec launch refused for hook %q: program %q contains a parent-directory segment; write its absolute path without \"..\" or a project-relative path", hook, arg)
	}
	resolved, err := resolveLauncher(in.root, arg)
	if err != nil {
		return "", err
	}
	if target, _, inside := projectPath(in.logical, in.root, resolved); inside {
		return "", fmt.Errorf("exec launch refused for hook %q: %q resolves to %q inside the project; name the script by its project path", hook, arg, target)
	}
	return resolved, nil
}

// resolveLauncher makes the program absolute: a bare name through PATH, a
// relative path against the project root.
func resolveLauncher(root, name string) (string, error) {
	switch {
	case filepath.IsAbs(name):
		return name, nil
	case filepath.Base(name) == name:
		path, found := lookPathFixed(filepath.SplitList(FixedHookPath(root)), name)
		if !found {
			return "", fmt.Errorf("exec launch: %q was not found on the fixed hook PATH; use an absolute program path", name)
		}
		return path, nil
	default:
		return filepath.Join(root, name), nil
	}
}
