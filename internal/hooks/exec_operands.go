// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	// endsOptions reports an option after which the next argument is the
	// script even when it starts with a dash (-b, as zsh reads it). It applies
	// to every shell name, because sh can be zsh.
	endsOptions func(option string) bool
	// checkValue vets the value of an option that takes one.
	checkValue func(option, value string) error
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

// interpreterFamilies are matched by prefix, longest names first.
var interpreterFamilies = []string{"nodejs", "node", "pythonw", "python", "pypy", "bash", "zsh", "dash", "ksh", "ash", "tcsh", "csh", "fish", "sh", "perl", "ruby"}

// interpreterFamily maps a lowercased base name to its family. The name is the
// family or the family followed by a version or architecture suffix that
// starts with a digit, dot, underscore or hyphen (bash5, python3.11-intel64,
// zsh-5.9), so name variants cannot escape recognition.
func interpreterFamily(base string) string {
	for _, family := range interpreterFamilies {
		rest, ok := strings.CutPrefix(base, family)
		if !ok {
			continue
		}
		if rest == "" || (strings.ContainsRune("0123456789._-", rune(rest[0])) && strings.Trim(rest, "abcdefghijklmnopqrstuvwxyz0123456789._-") == "") {
			return family
		}
	}
	return ""
}

// programBase is the lowercased base name without a Windows extension, so
// case and version variants (SH, Bash, perl5.34, nodejs) match their family.
func programBase(program string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(program)), ".exe")
}

func isEnv(program string) bool { return programBase(program) == "env" }

// commandWrappers launch another program with options mtix does not verify.
// One top-level env is looked through; every other wrapper is refused.
var commandWrappers = []string{"env", "nohup", "nice", "ionice", "xargs", "arch", "timeout", "stdbuf", "unbuffer", "setsid", "sudo", "doas", "command", "exec", "builtin", "time", "chrt", "taskset", "script", "flock", "watch", "caffeinate", "busybox"}

// projectRelative reports a relative path with a separator that stays inside
// the project after cleaning; ../ paths that leave it are PATH-style programs.
func projectRelative(program string) bool {
	if filepath.IsAbs(program) || !strings.ContainsRune(program, filepath.Separator) {
		return false
	}
	cleaned := filepath.Clean(program)
	return cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

// wrapperError reports a program that is a command wrapper, or nil. A
// project-relative path is a project file, never a wrapper.
func wrapperError(program string) error {
	if projectRelative(program) {
		return nil
	}
	base := programBase(program)
	for _, w := range commandWrappers {
		if base == w {
			return fmt.Errorf("wrapper %q is refused: a hook may use at most one top-level env and no other command wrapper, because the wrapped program and its options cannot be verified", w)
		}
	}
	return nil
}

// interpreterFor recognizes sh/bash/zsh/python/node/ruby/perl by program name.
// The known options are listed in the admin skill.
func interpreterFor(program string) (interpreter, bool) {
	base := programBase(program)
	switch interpreterFamily(base) {
	case "sh", "bash", "zsh", "dash", "ksh", "ash", "tcsh", "csh", "fish":
		letters := cluster("-+", "aefhmnuvxbC")
		long := oneOf("--posix", "--noprofile", "--norc")
		ip := interpreter{name: base, classify: classifier(func(o string) bool { return letters(o) || long(o) }, oneOf("-o", "+o", "-O", "+O"))}
		ip.endsOptions = func(o string) bool { return letters(o) && strings.Contains(o, "b") }
		return ip, true
	case "python", "pythonw", "pypy":
		return interpreter{name: base, classify: classifier(cluster("-", "BEIOqsSuvx"), oneOf("-W", "-X")), checkValue: pythonOptionValue}, true
	case "node", "nodejs":
		return interpreter{name: base, classify: classifier(oneOf("--no-warnings", "--enable-source-maps", "--trace-warnings", "--no-deprecation"), nil)}, true
	case "perl":
		return interpreter{name: base, classify: classifier(cluster("-", "wWTt"), nil)}, true
	case "ruby":
		return interpreter{name: base, classify: classifier(oneOf("-w", "-W0", "-W1", "-W2"), nil)}, true
	}
	return interpreter{}, false
}

// codeLoadingVariables are startup-file and library variables removed from the
// environment every hook inherits.
var codeLoadingVariables = []string{"BASH_ENV", "ENV", "NODE_OPTIONS", "PERL5OPT", "PERL5LIB", "RUBYOPT", "RUBYLIB", "PYTHONPATH", "PYTHONSTARTUP", "ZDOTDIR", "PYTHONUSERBASE", "PYTHONHOME", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH"}

func codeLoadingVariable(name string) bool {
	for _, v := range codeLoadingVariables {
		if v == name {
			return true
		}
	}
	return false
}

var (
	identifier     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	projectVarName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// loaderExact and loaderPrefixes name the variables that make an interpreter
// or the dynamic loader read startup files, modules or libraries, by family.
var (
	loaderExact    = []string{"BASH_ENV", "ENV", "ZDOTDIR", "SHELLOPTS", "BASHOPTS", "PS4", "PROMPT_COMMAND", "IFS", "CDPATH", "GCONV_PATH", "PATH", "FPATH", "fpath", "MODULE_PATH", "module_path", "GOFLAGS", "GOENV", "GOROOT", "GOPATH", "APPDATA", "LOCALAPPDATA", "USERPROFILE", "PHPRC", "PHP_INI_SCAN_DIR", "TCLLIBPATH", "CLASSPATH", "MAVEN_OPTS", "GRADLE_OPTS", "LESSOPEN", "PAGER", "EDITOR", "VISUAL", "SHELL", "COMSPEC", "PATHEXT"}
	loaderPrefixes = []string{"PYTHON", "NODE_", "PERL", "RUBY", "GEM_", "LD_", "DYLD_", "BASH_", "ZSH", "OPENSSL_", "MTIX_", "XDG_", "GIT_", "JAVA_", "JDK_", "LUA_", "DOTNET_", "CORECLR_", "COR_", "ERL_", "JULIA_", "R_PROFILE", "R_ENVIRON", "R_LIBS", "GO", "CGO_", "RUSTC_", "CARGO_", "NPM_CONFIG_"}
)

func loaderFamily(name string) bool {
	for _, v := range loaderExact {
		if v == name {
			return true
		}
	}
	for _, p := range loaderPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// allowedAssignment is the environment allowlist for an exec hook: plain data
// variables (LANG, LC_*, TZ, TERM, NO_COLOR) and project-specific names
// shaped like ^[A-Z][A-Z0-9_]*$ that are not in a loader family. HOME is
// accepted by name and checked by value (it must not point into the project).
func allowedAssignment(name string) bool {
	switch {
	case name == "HOME":
		return true
	case loaderFamily(name):
		return false
	case name == "LANG" || name == "LANGUAGE" || name == "TZ" || name == "TERM" || name == "NO_COLOR":
		return true
	}
	return strings.HasPrefix(name, "LC_") || projectVarName.MatchString(name)
}

// checkAssignment vets one env NAME=VALUE argument against the allowlist.
func checkAssignment(arg string) error {
	name, _, _ := strings.Cut(arg, "=")
	if !identifier.MatchString(name) {
		return fmt.Errorf("env assignment %q is refused: %q is not a valid identifier (env treats every argument with \"=\" before the program as an assignment)", arg, name)
	}
	if name == "PATH" {
		return fmt.Errorf("env assignment %q is refused: PATH is provided by the scrubbed environment and cannot be set by a hook", arg)
	}
	if strings.HasPrefix(name, "MTIX_") {
		return fmt.Errorf("env assignment %q is refused: %s is not on the environment allowlist, because MTIX_* variables are set by mtix (loop prevention, identity and sync credentials)", arg, name)
	}
	if !allowedAssignment(name) {
		return fmt.Errorf("env assignment %q is refused: %s is not on the environment allowlist (plain data variables, LC_*, and project names matching ^[A-Z][A-Z0-9_]*$ outside the interpreter and loader families)", arg, name)
	}
	return nil
}

// pythonOptionValue refuses the -X values that relocate cache files.
func pythonOptionValue(option, value string) error {
	if option == "-X" && strings.HasPrefix(value, "pycache_prefix") {
		return fmt.Errorf("python option -X %q is refused: it relocates files the interpreter loads", value)
	}
	return nil
}

// programIndex returns the index of the program, looking through env and the
// only env forms accepted: -u NAME, -- and assignments. As env does, every
// other argument containing "=" before the program is an assignment.
func programIndex(command []string) (int, error) {
	if len(command) == 0 {
		return -1, nil
	}
	if !isEnv(command[0]) {
		return 0, wrapperError(command[0])
	}
	afterDashes := false
	for j := 1; j < len(command); j++ {
		arg := command[j]
		switch {
		case !afterDashes && arg == "--":
			afterDashes = true
		case !afterDashes && strings.HasPrefix(arg, "-"):
			skip, err := envOption(command, j)
			if err != nil {
				return -1, err
			}
			j += skip
		case strings.Contains(arg, "="):
			if err := checkAssignment(arg); err != nil {
				return -1, err
			}
		default:
			return j, wrapperError(arg)
		}
	}
	return -1, fmt.Errorf("env names no program to run; a hook command must end in the program and its arguments")
}

// envOption vets one env option at command[j] and returns how many following
// arguments it consumes. Only -u NAME is accepted: -i and unsetting an MTIX_
// variable would remove what mtix sets for loop prevention and identity.
func envOption(command []string, j int) (int, error) {
	arg := command[j]
	switch arg {
	case "-u":
		if j+1 < len(command) && strings.HasPrefix(strings.ToUpper(command[j+1]), "MTIX_") {
			return 0, fmt.Errorf("env option -u %s is refused: it would remove a variable mtix sets for loop prevention and identity", command[j+1])
		}
		return 1, nil
	case "-i":
		return 0, fmt.Errorf("env option %q is refused: it would remove the MTIX_HOOK_ORIGIN loop guard", arg)
	}
	return 0, fmt.Errorf("env option %q is not supported; only -u NAME, -- and NAME=VALUE are accepted", arg)
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
	return scanOptions(ip, command, pi+1)
}

// nextOperand is the index j when it exists in the command, else -1.
func nextOperand(command []string, j int) (int, error) {
	if j < len(command) {
		return j, nil
	}
	return -1, nil
}

// scanOptions walks the options after the interpreter and returns the index of
// the script operand.
func scanOptions(ip interpreter, command []string, start int) (int, error) {
	for j := start; j < len(command); j++ {
		arg := command[j]
		switch {
		case arg == "--" || (ip.endsOptions != nil && ip.endsOptions(arg)):
			return nextOperand(command, j+1)
		case arg == "" || (arg[0] != '-' && arg[0] != '+'):
			return j, nil
		}
		switch ip.classify(arg) {
		case optValue:
			if ip.checkValue != nil && j+1 < len(command) {
				if err := ip.checkValue(arg, command[j+1]); err != nil {
					return -1, err
				}
			}
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
		// Not Join: cleaning would apply a parent segment to link text.
		path = root + string(filepath.Separator) + value
	}
	resolved = resolveDeepest(path, 0)
	rel, err := filepath.Rel(root, resolved)
	byName := err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	return resolved, lexical, lexical || byName || underRoot(root, resolved)
}

// underRoot reports whether path is the project root or lies under it by file
// identity: some existing ancestor of path is the same file as root. A string
// comparison alone is wrong on a case-insensitive volume, where a case-variant
// spelling names the same file.
func underRoot(root, path string) bool {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return false
	}
	for current := filepath.Clean(path); ; {
		if info, statErr := os.Stat(current); statErr == nil && os.SameFile(rootInfo, info) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

// maxLinkExpansions bounds how many links one resolution follows, so a cycle
// ends (the OS refuses such a path, so nothing runs through it).
const maxLinkExpansions = 64

// resolveDeepest resolves path the way the OS does: component by component,
// following each link before the next component is applied, so a parent-
// directory segment after a link goes to the link target's parent, not to the
// parent of the link's text. Components that do not exist yet are attached
// lexically, so a path is judged where it would land; a dangling link is
// followed by its target. depth is the number of links already followed.
func resolveDeepest(path string, depth int) string {
	if !filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	vol := filepath.VolumeName(path)
	root := vol + string(filepath.Separator)
	todo := splitPathComponents(path[len(vol):])
	cur, links := root, depth
	for len(todo) > 0 {
		comp := todo[0]
		todo = todo[1:]
		switch comp {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, comp)
		info, err := os.Lstat(next)
		if err != nil {
			return finishResolved(cur, append([]string{comp}, todo...))
		}
		if info.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		links++
		target, readErr := os.Readlink(next)
		if links > maxLinkExpansions || readErr != nil {
			return finishResolved(next, todo)
		}
		if filepath.IsAbs(target) {
			targetVol := filepath.VolumeName(target)
			cur = targetVol + string(filepath.Separator)
			target = target[len(targetVol):]
		}
		todo = append(splitPathComponents(target), todo...)
	}
	return finishResolved(cur, nil)
}

// finishResolved canonicalizes the part that exists and attaches the rest.
func finishResolved(existing string, rest []string) string {
	if resolved, err := filepath.EvalSymlinks(existing); err == nil {
		existing = resolved
	}
	return filepath.Join(append([]string{existing}, rest...)...)
}

// splitPathComponents splits on every separator the platform accepts.
func splitPathComponents(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == filepath.Separator })
}

// hasParentSegment reports a ".." component in a program path as written.
func hasParentSegment(p string) bool {
	return slices.Contains(splitPathComponents(p), "..")
}

// checkOperands refuses a launch that would run, by path, a file that is not
// one of the approved copies: an unapproved script operand, or any operand
// that reaches into the project through a link from outside it.
func checkOperands(h Hook, in *hookInputs) error {
	for i, arg := range h.Exec.Command {
		if _, staged := in.scripts[i]; staged {
			continue
		}
		value := arg
		if i != in.program && i != in.script {
			value, _ = splitOption(arg)
		}
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

// checkEnvAssignments refuses a HOME assignment through env whose value lies
// inside the project: shells and interpreters read startup files from HOME.
func checkEnvAssignments(command []string, program int, logical, root string) error {
	if program < 0 {
		return nil
	}
	for _, arg := range command[:program] {
		name, value, found := strings.Cut(arg, "=")
		if !found || name != "HOME" {
			continue
		}
		if _, _, inside := projectPath(logical, root, value); inside {
			return fmt.Errorf("env assignment %q is refused: HOME inside the project makes interpreters read startup files that are not approved", arg)
		}
	}
	return nil
}
