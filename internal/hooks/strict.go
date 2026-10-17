// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// strictModeKey names the host-level strict record in operator-local state.
// It is never a workspace configuration key.
const strictModeKey = "hooks-strict-mode"

type strictRecord struct {
	Enabled bool `json:"enabled"`
}

// programRecord is an operator's approval of one hook's program for strict
// mode, bound to the project, the hook name, the symlink-resolved program path
// and the digest of the hook's approved inputs.
type programRecord struct {
	Project string `json:"project"`
	Hook    string `json:"hook"`
	Program string `json:"program"`
	Digest  string `json:"digest"`
}

// StrictStatus reports strict program mode and the program approvals.
type StrictStatus struct {
	Enabled  bool              `json:"enabled"`
	Detail   string            `json:"detail"`
	Fix      string            `json:"fix"`
	Programs []ProgramApproval `json:"programs"`
}

// ProgramApproval reports one hook's program approval for strict mode.
type ProgramApproval struct {
	Hook     string `json:"hook"`
	Program  string `json:"program"`
	Approved bool   `json:"approved"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix"`
}

// ProgramTrustFix returns the command that approves a hook's program.
func ProgramTrustFix(name string) string {
	return "mtix hooks trust --program -- '" + strings.ReplaceAll(name, "'", "'\"'\"'") + "'"
}

// StrictEnabled reports the host-level strict flag. An absent record is off.
// A record that exists but cannot be used is an error, so callers fail closed.
func (a *Approvals) StrictEnabled() (bool, error) {
	var rec strictRecord
	present, reports, err := readApprovalRecord(a.state, strictModeKey, &rec)
	if err != nil {
		return false, fmt.Errorf("strict program mode record: %w", err)
	}
	if len(reports) != 0 {
		return false, fmt.Errorf("strict program mode record: %s", reports[0].Reason)
	}
	return present && rec.Enabled, nil
}

// SetStrict changes the host-level strict flag. It needs a terminal and is
// refused under an agent session marker; the write is atomic and private.
func (a *Approvals) SetStrict(on, terminal bool) error {
	if !terminal || a.session {
		return fmt.Errorf("changing strict program mode requires terminal stdin outside an agent session")
	}
	if err := a.state.WriteJSON(strictModeKey, strictRecord{Enabled: on}); err != nil {
		return fmt.Errorf("save strict program mode: %w", err)
	}
	return nil
}

// strictProgram returns the program's symlink-resolved absolute path. It
// refuses a program that does not resolve or that resolves inside the project:
// that is a script and must be pinned instead.
func strictProgram(h Hook, in *hookInputs, index int) (string, error) {
	launcher, err := resolveLauncher(in.root, h.Exec.Command[index])
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(launcher)
	if err != nil {
		return "", fmt.Errorf("program %q cannot be resolved: %w", launcher, err)
	}
	if _, _, inside := projectPath(in.logical, in.root, resolved); inside {
		return "", fmt.Errorf("program %q resolves to %q inside the project; a project file is a script and must be pinned instead", launcher, resolved)
	}
	return resolved, nil
}

// strictTarget returns the command index whose program strict mode must have
// approved, and whether any approval is needed. A launch wrapper that is not
// the known env in a PATH directory is itself the program to approve (index 0),
// whatever it runs; otherwise only a program of class other needs approval.
func strictTarget(h Hook, in *hookInputs) (index int, needed bool) {
	if in.program > 0 && !knownEnvWrapper(in.root, h.Exec.Command[0]) {
		return 0, true
	}
	if classifyProgram(h, in).Class == ClassOther {
		return in.program, true
	}
	return 0, false
}

// knownEnvWrapper reports a program that is env by exact name and is a bare
// name or an absolute path in one of the fixed PATH directories. A name alone
// is not trusted: a relative path, or a path outside PATH, is some other file.
func knownEnvWrapper(root, program string) bool {
	_, ok := knownEnvPin(root, program)
	return ok
}

// knownEnvPin is knownEnvWrapper that also returns the path to launch: the
// physical directory the wrapper was judged in plus its own name.
func knownEnvPin(root, program string) (string, bool) {
	return knownProgramPin(root, program, isEnv)
}

// ApproveProgram approves the hook's program for strict mode and returns the
// symlink-resolved path the record is bound to. It needs a terminal, is
// refused under an agent session marker, and applies to a program of class
// other only.
func (a *Approvals) ApproveProgram(h Hook, terminal bool) (string, error) {
	return a.ApproveProgramShowing(h, terminal, nil)
}

// ApproveProgramShowing is ApproveProgram that first resolves the program once,
// calls show with it, and then writes that same value, so what the operator is
// shown is what the record binds. show is not called when the gate refuses.
func (a *Approvals) ApproveProgramShowing(h Hook, terminal bool, show func(program string) error) (string, error) {
	if !terminal || a.session {
		return "", fmt.Errorf("program approval requires terminal stdin outside an agent session")
	}
	in, project, err := a.strictInputs(h)
	if err != nil {
		return "", err
	}
	index, needed := strictTarget(h, in)
	if !needed {
		return "", fmt.Errorf("hook %q runs a %s and needs no program approval", h.Name, classifyProgram(h, in).Class)
	}
	program, err := strictProgram(h, in, index)
	if err != nil {
		return "", fmt.Errorf("hook %q: %w", h.Name, err)
	}
	if show != nil {
		if err := show(program); err != nil {
			return "", err
		}
	}
	rec := programRecord{Project: project, Hook: h.Name, Program: program, Digest: in.digest}
	if err := a.state.WriteJSON(recordKey("prog-", project, h.Name), rec); err != nil {
		return "", fmt.Errorf("save program approval: %w", err)
	}
	return program, nil
}

func (a *Approvals) strictInputs(h Hook) (*hookInputs, string, error) {
	if h.Exec == nil || len(h.Exec.Command) == 0 {
		return nil, "", fmt.Errorf("hook %q has no command", h.Name)
	}
	logical, root, err := projectRoots(a.dir)
	if err != nil {
		return nil, "", err
	}
	project, err := projectIdentity(a.dir)
	if err != nil {
		return nil, "", err
	}
	in, err := collectHookInputs(h, logical, root)
	return in, project, err
}

// programApproved reports whether an approval record matches exactly this
// project, hook, resolved program and hook digest.
func (a *Approvals) programApproved(project, hook, program, digest string) (bool, error) {
	var rec programRecord
	present, reports, err := readApprovalRecord(a.state, recordKey("prog-", project, hook), &rec)
	if err != nil {
		return false, err
	}
	if len(reports) != 0 {
		return false, fmt.Errorf("program approval record: %s", reports[0].Reason)
	}
	if !present {
		return false, nil
	}
	return rec.Project == project && rec.Hook == hook && rec.Program == program && rec.Digest == digest, nil
}

// enforceStrict applies strict program mode to a prepared plan. With the mode
// off it does nothing. With it on, a program of class other launches only when
// an approval matches, and then launches by its resolved path.
func (a *Approvals) enforceStrict(h Hook, in *hookInputs, plan *ExecPlan) error {
	on, err := a.StrictEnabled()
	if err != nil {
		return fmt.Errorf("exec launch refused for hook %q: strict program mode state cannot be read: %w", h.Name, err)
	}
	index, needed := strictTarget(h, in)
	if !on {
		return nil
	}
	refuse := func(why string) error {
		return fmt.Errorf("exec launch refused for hook %q: strict program mode is on and %s; approve it on a terminal: %s", h.Name, why, ProgramTrustFix(h.Name))
	}
	skip := -1
	if needed {
		skip = index
	}
	if pinErr := pinKnownSlots(h, in, plan, skip); pinErr != nil {
		return refuse(pinErr.Error())
	}
	if !needed {
		return nil
	}
	program, err := strictProgram(h, in, index)
	if err != nil {
		return refuse(err.Error())
	}
	project, err := projectIdentity(a.dir)
	if err != nil {
		return refuse(err.Error())
	}
	ok, err := a.programApproved(project, h.Name, program, in.digest)
	if err != nil {
		return refuse("the program approval cannot be read: " + err.Error())
	}
	if !ok {
		return refuse(fmt.Sprintf("program %q is not approved", program))
	}
	plan.Argv[index] = program
	plan.Resolved[h.Exec.Command[index]] = program
	return nil
}

// pinKnownSlots pins every launch wrapper (env) and known interpreter slot to
// the physical directory it is judged in, in every strict-mode launch, so a
// link in the written path cannot be re-pointed between the check and the
// launch. The slot at skip is pinned as the approved program instead. Each
// slot is resolved once: the directory judged is the directory launched.
func pinKnownSlots(h Hook, in *hookInputs, plan *ExecPlan, skip int) error {
	if in.program > 0 && skip != 0 {
		pin, ok := knownEnvPin(in.root, plan.Argv[0])
		if !ok {
			return fmt.Errorf("launch wrapper %q is not the known env in a fixed PATH directory", plan.Argv[0])
		}
		plan.Argv[0] = pin
	}
	if skip != in.program && classifyProgram(h, in).Class == ClassInterpreterScript {
		pin, ok := knownInterpreterPin(in.root, plan.Argv[in.program])
		if !ok {
			return fmt.Errorf("interpreter %q is not a known interpreter in a fixed PATH directory", plan.Argv[in.program])
		}
		plan.Argv[in.program] = pin
	}
	return nil
}

// StrictStatus reports strict mode for the configured exec hooks, with the
// exact corrective command for each hook whose program is not approved.
func (a *Approvals) StrictStatus(cfg Config) StrictStatus {
	st := StrictStatus{Programs: []ProgramApproval{}}
	on, err := a.StrictEnabled()
	if err != nil {
		st.Detail = "strict program mode state cannot be read; launches that need it are refused: " + err.Error()
		st.Fix = "On a terminal outside an agent session, rewrite the record: mtix hooks strict on (or mtix hooks strict off)"
		return st
	}
	st.Enabled = on
	var fixes []string
	for _, h := range cfg.Hooks {
		if h.Exec == nil || !slices.Contains(h.Deliver, AdapterExec) {
			continue
		}
		entry, listed := a.programApproval(h)
		if !listed {
			continue
		}
		if on && !entry.Approved {
			fixes = append(fixes, entry.Fix)
		}
		st.Programs = append(st.Programs, entry)
	}
	st.Detail, st.Fix = strictSummary(on, fixes)
	return st
}

func strictSummary(on bool, fixes []string) (detail, fix string) {
	switch {
	case !on:
		return "strict program mode is off (default); enable it on a terminal with: mtix hooks strict on", ""
	case len(fixes) > 0:
		return "strict program mode is on; hooks whose program is not approved are refused at launch", strings.Join(fixes, "; ")
	}
	return "strict program mode is on; every hook program is a pinned script, a known interpreter or approved", ""
}

func (a *Approvals) programApproval(h Hook) (ProgramApproval, bool) {
	entry := ProgramApproval{Hook: h.Name, Fix: ProgramTrustFix(h.Name)}
	in, project, err := a.strictInputs(h)
	if err != nil {
		entry.Detail = "the hook cannot be inspected: " + err.Error()
		return entry, true
	}
	index, needed := strictTarget(h, in)
	if !needed {
		return entry, false
	}
	program, err := strictProgram(h, in, index)
	if err != nil {
		entry.Program = h.Exec.Command[index]
		entry.Detail = err.Error()
		return entry, true
	}
	entry.Program = program
	ok, err := a.programApproved(project, h.Name, program, in.digest)
	switch {
	case err != nil:
		entry.Detail = "the program approval cannot be read: " + err.Error()
	case ok:
		entry.Approved, entry.Fix, entry.Detail = true, "", "the program is approved for strict mode"
	default:
		entry.Detail = "the program is not approved for strict mode"
	}
	return entry, true
}
