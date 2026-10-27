// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

// An exec hook child gets an explicit environment (ADR-007 P0.3, TM s7, I4,
// I5): an allowlist of inherited variables, the variables the hook names in
// env-pass, a fixed PATH, the mtix markers, and the hook's own author id.
// Everything else the dispatcher had is dropped.

// localeInherited are the exact locale category names a hook may inherit. No
// LC_ prefix is matched, so LC_API_TOKEN and the like are never admitted.
var localeInherited = []string{"LC_ALL", "LC_CTYPE", "LC_COLLATE", "LC_MESSAGES", "LC_MONETARY", "LC_NUMERIC", "LC_TIME", "LC_ADDRESS", "LC_IDENTIFICATION", "LC_MEASUREMENT", "LC_NAME", "LC_PAPER", "LC_TELEPHONE"}

var unixInherited = append([]string{"HOME", "USER", "LOGNAME", "LANG", "TZ", "TMPDIR", "TERM", "NO_COLOR"}, localeInherited...)

var windowsInherited = append([]string{"HOME", "USER", "LOGNAME", "LANG", "TZ", "TERM", "NO_COLOR", "SystemRoot", "ComSpec", "PATHEXT", "USERPROFILE", "TEMP", "TMP"}, localeInherited...)

// windowsCarveOut names the default Windows variables that are in the loader
// list yet are passed on purpose (ruling D3): the shell and the system need
// them to start. They are the only names the deny list does not apply to.
var windowsCarveOut = []string{"ComSpec", "PATHEXT", "USERPROFILE"}

// envNameMatches compares variable names; Windows names are case-insensitive.
func envNameMatches(goos, a, b string) bool {
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func inheritedByDefault(goos, name string) bool {
	names := unixInherited
	if goos == "windows" {
		names = windowsInherited
	}
	for _, n := range names {
		if !envNameMatches(goos, n, name) {
			continue
		}
		// The deny list always wins, except for the documented Windows carve-out.
		return envPassProblem(name) == "" || carvedOut(goos, name)
	}
	return false
}

func carvedOut(goos, name string) bool {
	if goos != "windows" {
		return false
	}
	for _, n := range windowsCarveOut {
		if envNameMatches(goos, n, name) {
			return true
		}
	}
	return false
}

// credentialShaped reports names that look like secrets or connection strings.
func credentialShaped(name string) bool {
	upper := strings.ToUpper(name)
	for _, suffix := range []string{"_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "_PASSWD", "_CREDENTIAL", "_CREDENTIALS"} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	for _, part := range []string{"DSN", "SECRET", "PASSWORD", "PASSWD", "APIKEY", "API_KEY", "PRIVATE_KEY", "ACCESS_KEY"} {
		if strings.Contains(upper, part) {
			return true
		}
	}
	switch upper {
	case "KEY", "TOKEN", "CREDENTIALS":
		return true
	}
	return false
}

// envPassProblem explains why a name cannot be passed through to a hook, or
// returns "" when it can. The deny list always wins; it reuses the loader and
// startup families that environment assignments are checked against.
func envPassProblem(name string) string {
	upper := strings.ToUpper(name)
	switch {
	case !identifier.MatchString(name):
		return "is not a valid variable name"
	case strings.HasPrefix(upper, "MTIX_"):
		return "is an mtix variable; mtix sets its own"
	case upper == "PATH":
		return "cannot be passed; PATH is fixed by mtix"
	case loaderFamily(name) || loaderFamily(upper) || codeLoadingVariable(upper):
		return "makes a program load code or configuration at startup"
	case credentialShaped(name):
		return "looks like a credential; read secrets from an operator file inside the script"
	}
	return ""
}

// windowsSystemRoot is the Windows directory used for the hook PATH. It is a
// constant: the dispatcher SystemRoot variable is not trusted for it.
const windowsSystemRoot = `C:\Windows`

const (
	maxEnvPassNames  = 32
	maxEnvPassLength = 128
)

// envPassCheck validates every env-pass name of a hook, and bounds the list.
func envPassCheck(h Hook) string {
	if h.Exec == nil {
		return ""
	}
	if len(h.Exec.EnvPass) > maxEnvPassNames {
		return fmt.Sprintf("exec.env-pass lists %d names; at most %d are allowed", len(h.Exec.EnvPass), maxEnvPassNames)
	}
	for _, name := range h.Exec.EnvPass {
		if len(name) > maxEnvPassLength {
			return fmt.Sprintf("exec.env-pass has a name longer than %d bytes", maxEnvPassLength)
		}
	}
	for _, name := range h.Exec.EnvPass {
		if problem := envPassProblem(name); problem != "" {
			return fmt.Sprintf("exec.env-pass name %q %s", name, problem)
		}
	}
	return ""
}

// childEnvironment builds the environment of an exec hook child.
func childEnvironment(parent []string, goos string, delivery Delivery, scriptPath, pathValue string) []string {
	h, event := delivery.Hook, delivery.EventJSON
	var pass []string
	if h.Exec != nil {
		pass = h.Exec.EnvPass
	}
	out := make([]string, 0, len(parent)+6)
	for _, kv := range parent {
		name, _, found := strings.Cut(kv, "=")
		if !found || name == "" {
			continue
		}
		if inheritedByDefault(goos, name) || passedThrough(goos, pass, name) {
			out = append(out, kv)
		}
	}
	out = append(out,
		"PATH="+pathValue,
		"MTIX_HOOK_SCRIPT_PATH="+scriptPath,
		"MTIX_EVENT="+string(event),
		"MTIX_HOOK_ORIGIN="+h.Name)
	if author := h.Match.ToAgent; validChildAuthor(author) {
		out = append(out, "MTIX_AUTHOR_ID="+author)
	}
	return append(out, h.wakeEnv...)
}

func passedThrough(goos string, pass []string, name string) bool {
	if envPassProblem(name) != "" {
		return false
	}
	for _, n := range pass {
		if envNameMatches(goos, n, name) {
			return true
		}
	}
	return false
}

// validChildAuthor accepts a non-empty agent id without control characters.
func validChildAuthor(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// FixedHookPath is the PATH of an exec hook child: the directory of the
// running mtix binary (unless it lies inside the project), then system
// directories. It has no relative entry and no project directory.
func FixedHookPath(root string) string {
	exe, err := os.Executable()
	if err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
	}
	return fixedPath(root, exe, runtime.GOOS)
}

func fixedPath(root, exe, goos string) string {
	var dirs []string
	if exe != "" && filepath.IsAbs(exe) {
		if dir := filepath.Dir(exe); !insideDirectory(root, dir) {
			dirs = append(dirs, dir)
		}
	}
	sep := ":"
	switch goos {
	case "windows":
		sep = ";"
		system := windowsSystemRoot
		dirs = append(dirs, filepath.Join(system, "System32"), system)
	case "darwin":
		// Order matters: the first match wins, so Homebrew comes before the system copies.
		dirs = append(dirs, "/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	default:
		dirs = append(dirs, "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	}
	return strings.Join(dirs, sep)
}

// insideDirectory reports whether path is root or lies inside it.
func insideDirectory(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// lookPathFixed finds an executable regular file for name in dirs, never
// consulting the dispatcher's PATH.
func lookPathFixed(dirs []string, name string) (string, bool) {
	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = []string{".exe", ".cmd", ".bat", ".com", ""}
	}
	for _, dir := range dirs {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		for _, ext := range exts {
			candidate := filepath.Join(dir, name+ext)
			info, err := os.Stat(candidate)
			if err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0) {
				return candidate, true
			}
		}
	}
	return "", false
}
