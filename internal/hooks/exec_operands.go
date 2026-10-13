// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// interpreter describes how a command line selects its script operand.
type interpreter struct {
	// commandOption reports an option that makes the next operand program text
	// rather than a script file.
	commandOption func(string) bool
	// valueOptions take their value as the following argument.
	valueOptions map[string]bool
}

var shellInterpreter = interpreter{
	commandOption: func(o string) bool { return shortCluster.MatchString(o) && strings.Contains(o, "c") },
	valueOptions:  map[string]bool{"-o": true, "+o": true, "-O": true, "+O": true},
}

var shortCluster = regexp.MustCompile(`^-[A-Za-z]+$`)

var envAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func optionIn(names ...string) func(string) bool {
	return func(o string) bool {
		for _, n := range names {
			if o == n {
				return true
			}
		}
		return false
	}
}

// interpreterFor recognizes sh/bash/zsh/python/node/ruby/perl by program name.
func interpreterFor(program string) (interpreter, bool) {
	base := filepath.Base(program)
	switch {
	case base == "sh" || base == "bash" || base == "zsh" || base == "dash" || base == "ksh" || base == "ash":
		return shellInterpreter, true
	case strings.HasPrefix(base, "python"):
		return interpreter{commandOption: optionIn("-c", "-m"), valueOptions: map[string]bool{"-W": true, "-X": true, "-Q": true}}, true
	case base == "node":
		return interpreter{commandOption: optionIn("-e", "-p", "--eval", "--print"), valueOptions: map[string]bool{"-r": true, "--require": true, "--loader": true, "--import": true}}, true
	case base == "perl":
		return interpreter{commandOption: optionIn("-e", "-E"), valueOptions: map[string]bool{"-I": true}}, true
	case base == "ruby":
		return interpreter{commandOption: optionIn("-e"), valueOptions: map[string]bool{"-r": true, "-I": true}}, true
	}
	return interpreter{}, false
}

// programIndex returns the index of the program, looking through env and its
// own options and NAME=VALUE pairs; -1 if there is none.
func programIndex(command []string) int {
	if len(command) == 0 {
		return -1
	}
	if filepath.Base(command[0]) != "env" {
		return 0
	}
	valued := map[string]bool{"-u": true, "--unset": true, "-C": true, "--chdir": true}
	for j := 1; j < len(command); j++ {
		arg := command[j]
		switch {
		case arg == "--":
			if j+1 < len(command) {
				return j + 1
			}
			return -1
		case valued[arg]:
			j++
		case strings.HasPrefix(arg, "-") || envAssignment.MatchString(arg):
		default:
			return j
		}
	}
	return -1
}

// scriptOperandIndex returns the index of the argument a launch runs as
// code: the program itself when it names a path, or an interpreter's first
// non-option operand. It is -1 when the command runs program text or a bare
// program name only.
func scriptOperandIndex(command []string) int {
	pi := programIndex(command)
	if pi < 0 {
		return -1
	}
	ip, ok := interpreterFor(command[pi])
	if !ok {
		if filepath.IsAbs(command[pi]) || strings.ContainsRune(command[pi], filepath.Separator) {
			return pi
		}
		return -1
	}
	for j := pi + 1; j < len(command); j++ {
		arg := command[j]
		switch {
		case arg == "--":
			if j+1 < len(command) {
				return j + 1
			}
			return -1
		case ip.commandOption(arg):
			return -1
		case ip.valueOptions[arg]:
			j++
		case strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "+"):
		default:
			return j
		}
	}
	return -1
}

// splitOption separates the value of an --opt=VALUE argument; prefix is empty
// for any other argument.
func splitOption(arg string) (value, prefix string) {
	if idx := strings.Index(arg, "="); strings.HasPrefix(arg, "-") && idx > 0 {
		return arg[idx+1:], arg[:idx+1]
	}
	return arg, ""
}

// checkOperands refuses a launch that would run, by path, a file that is not
// one of the approved copies: an unapproved script operand, or any operand
// that is a link from outside the project to a project file.
func checkOperands(h Hook, in *hookInputs, root string) error {
	command := h.Exec.Command
	script := scriptOperandIndex(command)
	for i, arg := range command {
		if _, staged := in.scripts[i]; staged {
			continue
		}
		value, _ := splitOption(arg)
		_, inside, err := projectScriptLocation(root, root, value)
		if err != nil {
			return err
		}
		if i == script && inside {
			return fmt.Errorf("exec launch refused for hook %q: script %q is not an approved project file; create it and approve the hook again", h.Name, arg)
		}
		if resolved, linked := linksIntoProject(root, value, inside); linked {
			return fmt.Errorf("exec launch refused for hook %q: %q resolves to %q inside the project and is not an approved file", h.Name, arg, resolved)
		}
	}
	return nil
}

// linksIntoProject reports a path outside the project whose symlink-resolved
// target lies inside it.
func linksIntoProject(root, value string, inside bool) (string, bool) {
	if inside || value == "" {
		return "", false
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return insideRoot(root, path)
}

// insideRoot resolves symlinks and reports whether the target lies in the
// project; it returns the resolved path.
func insideRoot(root, path string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, resolved)
	return resolved, err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
