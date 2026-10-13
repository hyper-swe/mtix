// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type optionKind int

const (
	optUnknown optionKind = iota
	optFlag
	optValue // takes the following argument as its value
)

// interpreter classifies the options that may precede the script operand.
// Anything it does not recognize is refused: the script position is never
// guessed.
type interpreter struct {
	name     string
	classify func(option string) optionKind
}

var envAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// cluster matches one of the prefixes followed by letters from the set.
func cluster(prefixes, letters string) func(string) bool {
	return func(o string) bool {
		if len(o) < 2 || !strings.ContainsRune(prefixes, rune(o[0])) {
			return false
		}
		return strings.Trim(o[1:], letters) == ""
	}
}

func oneOf(names ...string) func(string) bool {
	return func(o string) bool {
		for _, n := range names {
			if o == n {
				return true
			}
		}
		return false
	}
}

func classifier(flags, values func(string) bool) func(string) optionKind {
	return func(o string) optionKind {
		switch {
		case values != nil && values(o):
			return optValue
		case flags(o):
			return optFlag
		}
		return optUnknown
	}
}

// interpreterFor recognizes sh/bash/zsh/python/node/ruby/perl by program name.
// The known options are listed in the admin skill.
func interpreterFor(program string) (interpreter, bool) {
	base := filepath.Base(program)
	switch {
	case oneOf("sh", "bash", "zsh", "dash", "ksh", "ash")(base):
		letters := cluster("-+", "aefhmnuvxbC")
		long := oneOf("--posix", "--noprofile", "--norc")
		return interpreter{base, classifier(func(o string) bool { return letters(o) || long(o) }, oneOf("-o", "+o", "-O", "+O"))}, true
	case strings.HasPrefix(base, "python"):
		return interpreter{base, classifier(cluster("-", "BEIOqsSuvx"), oneOf("-W", "-X"))}, true
	case base == "node":
		return interpreter{base, classifier(oneOf("--no-warnings", "--enable-source-maps", "--trace-warnings", "--no-deprecation"), nil)}, true
	case base == "perl":
		return interpreter{base, classifier(cluster("-", "wWTt"), nil)}, true
	case base == "ruby":
		return interpreter{base, classifier(oneOf("-w", "-W0", "-W1", "-W2"), nil)}, true
	}
	return interpreter{}, false
}

// programIndex returns the index of the program, looking through env and the
// only env forms accepted: -i, -u NAME, -- and NAME=VALUE.
func programIndex(command []string) (int, error) {
	if len(command) == 0 {
		return -1, nil
	}
	if filepath.Base(command[0]) != "env" {
		return 0, nil
	}
	for j := 1; j < len(command); j++ {
		arg := command[j]
		switch {
		case arg == "--":
			if j+1 < len(command) {
				return j + 1, nil
			}
			return -1, nil
		case arg == "-u":
			j++
		case arg == "-i" || envAssignment.MatchString(arg):
		case strings.HasPrefix(arg, "-"):
			return -1, fmt.Errorf("env option %q is not supported; only -i, -u NAME, -- and NAME=VALUE are accepted", arg)
		default:
			return j, nil
		}
	}
	return -1, nil
}

// scriptOperand returns the index of the argument a launch runs as code: the
// program itself when it names a path, or an interpreter's first non-option
// operand. It is -1 for a bare program name that is not an interpreter. An
// option that is not in the interpreter's table before the script is an error.
func scriptOperand(command []string) (int, error) {
	pi, err := programIndex(command)
	if err != nil || pi < 0 {
		return -1, err
	}
	ip, ok := interpreterFor(command[pi])
	if !ok {
		if filepath.IsAbs(command[pi]) || strings.ContainsRune(command[pi], filepath.Separator) {
			return pi, nil
		}
		return -1, nil
	}
	for j := pi + 1; j < len(command); j++ {
		arg := command[j]
		if arg == "--" {
			if j+1 < len(command) {
				return j + 1, nil
			}
			return -1, nil
		}
		if arg == "" || (arg[0] != '-' && arg[0] != '+') {
			return j, nil
		}
		switch ip.classify(arg) {
		case optValue:
			j++
		case optFlag:
		default:
			return -1, fmt.Errorf("option %q before the script is not recognized for %s; use only the documented options and a script file", arg, ip.name)
		}
	}
	return -1, nil
}

// splitOption separates the value of an --opt=VALUE or NAME=VALUE argument;
// prefix is empty for any other argument.
func splitOption(arg string) (value, prefix string) {
	if idx := strings.Index(arg, "="); idx > 0 && (strings.HasPrefix(arg, "-") || envAssignment.MatchString(arg)) {
		return arg[idx+1:], arg[:idx+1]
	}
	return arg, ""
}

// projectPath classifies a path against both the entered (logical) and the
// resolved project root. resolved follows links through the deepest existing
// parent, so a path that does not exist yet is judged where it would land.
func projectPath(logical, root, value string) (resolved string, lexical, inside bool) {
	if value == "" {
		return "", false, false
	}
	_, in, err := projectScriptLocation(logical, root, value)
	lexical = err == nil && in
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	resolved = resolveDeepest(path, 0)
	rel, err := filepath.Rel(root, resolved)
	return resolved, lexical, lexical || (err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolveDeepest resolves links up to the deepest existing parent and
// re-attaches the rest, following a dangling link by its target.
func resolveDeepest(path string, depth int) string {
	if depth > 32 {
		return path
	}
	current, rest := filepath.Clean(path), ""
	for {
		if _, err := os.Lstat(current); err == nil {
			return filepath.Join(resolveExisting(current, depth), rest)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
	}
}

// resolveExisting resolves a path that exists; a dangling link is followed by
// its target.
func resolveExisting(current string, depth int) string {
	if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if target, rerr := os.Readlink(current); rerr == nil {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(current), target)
			}
			return resolveDeepest(target, depth+1)
		}
	}
	if resolved, err := filepath.EvalSymlinks(current); err == nil {
		return resolved
	}
	return current
}

// checkOperands refuses a launch that would run, by path, a file that is not
// one of the approved copies: an unapproved script operand, or any operand
// that reaches into the project through a link from outside it.
func checkOperands(h Hook, in *hookInputs) error {
	for i, arg := range h.Exec.Command {
		if _, staged := in.scripts[i]; staged {
			continue
		}
		value, _ := splitOption(arg)
		resolved, lexical, inside := projectPath(in.logical, in.root, value)
		switch {
		case i == in.script && inside:
			return fmt.Errorf("exec launch refused for hook %q: script %q is not an approved project file; create it and approve the hook again", h.Name, arg)
		case inside && !lexical:
			return fmt.Errorf("exec launch refused for hook %q: %q resolves to %q inside the project and is not an approved file", h.Name, arg, resolved)
		}
	}
	return nil
}
