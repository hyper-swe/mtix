// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Program classes of an exec hook (ADR-007 C-P02).
const (
	// ClassPinnedScript: the program is an approved project script.
	ClassPinnedScript = "pinned-script"
	// ClassInterpreterScript: a known interpreter runs an approved project script.
	ClassInterpreterScript = "interpreter-script"
	// ClassOther: any other program; mtix does not pin or inspect it.
	ClassOther = "other"
	// ClassUnknown: the program could not be classified; review it.
	ClassUnknown = "unknown"
)

// ProgramReview classifies an exec hook's program. The notice is advisory: it
// never changes whether or how the hook launches.
type ProgramReview struct {
	Class   string `json:"class"`
	Needed  bool   `json:"review_needed"`
	Program string `json:"program"`
	Detail  string `json:"detail"`
	Fix     string `json:"fix"`
}

// ReviewProgram classifies the hook's program from the same inputs the
// approval digest covers, looking through the one allowed top-level env.
func (a *Approvals) ReviewProgram(h Hook) (ProgramReview, error) {
	if h.Exec == nil || len(h.Exec.Command) == 0 {
		return ProgramReview{}, fmt.Errorf("program review: hook %q has no command", h.Name)
	}
	logical, root, err := projectRoots(a.dir)
	if err != nil {
		return ProgramReview{}, err
	}
	in, err := collectHookInputs(h, logical, root)
	if err != nil {
		return ProgramReview{}, err
	}
	return classifyProgram(h, in), nil
}

// reviewOrUnknown classifies the hook, failing closed: a hook that cannot be
// classified gets a review that asks the operator to look at it.
func (a *Approvals) reviewOrUnknown(h Hook) ProgramReview {
	classify := a.classify
	if classify == nil {
		classify = a.ReviewProgram
	}
	review, err := classify(h)
	if err != nil {
		return ProgramReview{
			Class:  ClassUnknown,
			Needed: true,
			Detail: fmt.Sprintf("hook %q: the program could not be classified; review it: %v", h.Name, err),
			Fix:    "Run the program through a pinned project script (a script file in the project that calls it), then approve the hook again: " + TrustFix(h.Name),
		}
	}
	return review
}

func classifyProgram(h Hook, in *hookInputs) ProgramReview {
	command := h.Exec.Command
	if _, pinned := in.scripts[in.program]; pinned {
		return ProgramReview{Class: ClassPinnedScript, Program: command[in.program], Detail: "the program is a pinned project script"}
	}
	if knownInterpreterProgram(in.root, command[in.program]) && in.script >= 0 {
		if _, pinned := in.scripts[in.script]; pinned {
			return ProgramReview{Class: ClassInterpreterScript, Program: command[in.program], Detail: "a known interpreter runs a pinned project script"}
		}
	}
	program := reviewProgramPath(in.root, command[in.program])
	return ProgramReview{
		Class:   ClassOther,
		Needed:  true,
		Program: program,
		Detail:  fmt.Sprintf("hook %q runs %s as written; mtix pins project script files only and does not inspect this program or what it runs", h.Name, program),
		Fix:     "Run the program through a pinned project script (a script file in the project that calls it), then approve the hook again: " + TrustFix(h.Name),
	}
}

// reviewProgramPath reports the program by its symlink-resolved path when it
// can be resolved, else as written.
func reviewProgramPath(root, program string) string {
	path := program
	switch {
	case filepath.IsAbs(program):
	case filepath.Base(program) == program:
		found, ok := lookPathFixed(filepath.SplitList(FixedHookPath(root)), program)
		if !ok {
			return program
		}
		path = found
	default:
		path = filepath.Join(root, program)
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// reviewInterpreterName matches a known interpreter exactly: the family name
// with an optional version of digits and dots, after case-folding and one
// trailing .exe, .cmd or .bat (Windows-style names). This name matching is
// the review's own; launch matching is separate and unchanged.
var reviewInterpreterName = regexp.MustCompile(`^(sh|bash|zsh|dash|ksh|ash|tcsh|csh|fish|python|pythonw|pypy|node|nodejs|perl|ruby)[0-9.]*$`)

func reviewBaseName(program string) string {
	base := strings.ToLower(filepath.Base(program))
	for _, ext := range []string{".exe", ".cmd", ".bat"} {
		if trimmed, ok := strings.CutSuffix(base, ext); ok {
			return trimmed
		}
	}
	return base
}

// knownInterpreterProgram reports a program that is a known interpreter by
// exact name AND is a bare name or an absolute path in one of the PATH
// directories at approval time. Any other path (relative, or an absolute
// path outside PATH) is not trusted by its name.
func knownInterpreterProgram(root, program string) bool {
	_, ok := knownInterpreterPin(root, program)
	return ok
}

// knownInterpreterPin is knownInterpreterProgram that also returns the path to
// launch: the physical directory it was judged in plus the program's own name.
// The directory is resolved once, so the judged and the launched directory are
// the same.
func knownInterpreterPin(root, program string) (string, bool) {
	return knownProgramPin(root, program, func(p string) bool { return reviewInterpreterName.MatchString(reviewBaseName(p)) })
}

// knownProgramPin decides a program by name and by the physical directory that
// holds it (a fixed PATH directory), and returns that directory joined with the
// program's own name. A bare name is looked up on the fixed PATH and is
// returned as written.
func knownProgramPin(root, program string, nameKnown func(string) bool) (string, bool) {
	if !nameKnown(program) {
		return "", false
	}
	if filepath.IsAbs(program) {
		dir := programDirectory(program)
		if !inPathDirectory(root, dir) {
			return "", false
		}
		return filepath.Join(dir, filepath.Base(program)), true
	}
	if isBareName(program) {
		return program, true
	}
	return "", false
}

// programDirectory returns the physical directory that holds the program: the
// text before the last separator, resolved link by link, so a parent-directory
// segment after a link is applied where the OS applies it. The final component
// is left alone, so a program that is itself a link (a multi-call binary) keeps
// the name it is run by.
func programDirectory(program string) string {
	cut := strings.LastIndexAny(program, "/"+string(filepath.Separator))
	if cut <= 0 {
		return resolveDeepest(program[:cut+1], 0)
	}
	return resolveDeepest(program[:cut], 0)
}

// isBareName reports a program written without any path separator, using the
// rule resolveLauncher uses to look a name up on PATH: filepath.Base returns
// it unchanged. On Windows that counts a forward slash as a separator, as
// the launcher does, so a relative spelling is never trusted by its name.
func isBareName(program string) bool { return filepath.Base(program) == program }

func inPathDirectory(root, dir string) bool {
	for _, entry := range filepath.SplitList(FixedHookPath(root)) {
		if entry != "" && sameDirectory(entry, dir) {
			return true
		}
	}
	return false
}

func sameDirectory(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
