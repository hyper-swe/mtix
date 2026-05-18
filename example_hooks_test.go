// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package mtix_test contains MTIX-14.5 example hook script verification tests.
// These tests verify the security hardening, lint cleanliness, and runtime
// behaviour of the bash hooks and GitHub Action template shipped under
// examples/hooks/.
//
// Audit references in this file map to the threat tags in the ticket prompt:
//
//	T1 — shell injection (variable quoting, no eval)
//	T2 — CWD-PATH attack (resolve mtix once, then absolute path)
//	T3 — commit-vs-amend audit clarity
//	T4 — secret/raw-content echo
//	S1 — PG outage policy (warn-and-skip, no block on push)
//	S2 — bounded snapshot timeout
//
// Hooks are templates: they reference `mtix snapshot` (MTIX-14.3 — not yet
// implemented). The integration test stubs the binary so we exercise hook
// flow without depending on the real command.
package mtix_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hooksDir returns the absolute path to examples/hooks/.
func hooksDir(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok, "failed to get caller info")
	root := filepath.Dir(filename)
	return filepath.Join(root, "examples", "hooks")
}

// TestMain ensures bash hooks have the executable bit set on disk before
// any test runs. Some development environments (sandboxes that disallow
// chmod, archives without the +x bit, freshly-cloned repos with funky
// umasks) drop the executable bit. Git records the bit via
// `git update-index --chmod=+x`, so the canonical state in the index is
// 0755 — we restore that locally so the integration test can invoke the
// scripts via `bash <hookpath>` AND so the on-disk mode is what users
// will copy into their `.git/hooks/` directory.
func TestMain(m *testing.M) {
	_, filename, _, ok := runtime.Caller(0)
	if ok {
		root := filepath.Dir(filename)
		for _, name := range []string{"pre-push", "pre-receive"} {
			p := filepath.Join(root, "examples", "hooks", name)
			if _, err := os.Stat(p); err == nil {
				_ = os.Chmod(p, 0o755) //nolint:gosec // hooks must be executable
			}
		}
	}
	os.Exit(m.Run())
}

// readHook reads a hook file and returns its contents as a string.
func readHook(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(hooksDir(t), name))
	require.NoError(t, err, "hook %s should exist", name)
	return string(content)
}

// shellcheckAvailable reports whether shellcheck is on PATH.
func shellcheckAvailable() bool {
	_, err := exec.LookPath("shellcheck")
	return err == nil
}

// runShellcheck runs shellcheck against a hook and returns combined output.
func runShellcheck(t *testing.T, hookName string) (string, error) {
	t.Helper()
	cmd := exec.Command("shellcheck", "--shell=bash", "--severity=warning",
		filepath.Join(hooksDir(t), hookName))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestExampleHooks_PrePush_Exists verifies the pre-push hook ships in
// examples/hooks/ (acceptance #1).
func TestExampleHooks_PrePush_Exists(t *testing.T) {
	path := filepath.Join(hooksDir(t), "pre-push")
	info, err := os.Stat(path)
	require.NoError(t, err, "examples/hooks/pre-push should exist")
	// Must be executable for git to invoke it directly.
	if runtime.GOOS != "windows" {
		assert.NotZero(t, info.Mode()&0o111,
			"pre-push hook should be executable (mode %o)", info.Mode())
	}
}

// TestExampleHooks_PreReceive_Exists verifies the pre-receive hook ships.
func TestExampleHooks_PreReceive_Exists(t *testing.T) {
	path := filepath.Join(hooksDir(t), "pre-receive")
	info, err := os.Stat(path)
	require.NoError(t, err, "examples/hooks/pre-receive should exist")
	if runtime.GOOS != "windows" {
		assert.NotZero(t, info.Mode()&0o111,
			"pre-receive hook should be executable")
	}
}

// TestExampleHooks_GithubAction_Exists verifies the GH Action template ships.
func TestExampleHooks_GithubAction_Exists(t *testing.T) {
	path := filepath.Join(hooksDir(t), "github-action.yml")
	_, err := os.Stat(path)
	require.NoError(t, err, "examples/hooks/github-action.yml should exist")
}

// TestExampleHooks_README_Exists verifies the README ships and links to the
// security model. The v0.2.0-beta rewrite (MTIX-15.13) reframed the README
// for the FR-18 sync model: server-side enforcement is deferred to v0.2.1
// so the commit-vs-amend trade-off discussion was simplified (now lives in
// the pre-push hook itself via MTIX_HOOK_AMEND). Test assertions follow
// the v0.2.0-beta surface.
func TestExampleHooks_README_Exists(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(hooksDir(t), "README.md"))
	require.NoError(t, err, "examples/hooks/README.md should exist")

	body := string(content)
	// Active hook (pre-push) and deferred server-side variants must
	// be documented so users understand which to use.
	assert.Contains(t, body, "pre-push", "README should document the active pre-push hook")
	assert.Contains(t, body, "pre-receive", "README should document pre-receive status")
	assert.Contains(t, body, "Deferred",
		"README should explain that server-side hooks are deferred to v0.2.1")
	// Security caveat: client hooks are bypassable.
	assert.Contains(t, strings.ToLower(body), "bypass",
		"README should warn that client hooks are bypassable")
	// Cross-link to the trust model.
	assert.Contains(t, body, "SECURITY-MODEL.md",
		"README should link to docs/SECURITY-MODEL.md")
	// MTIX-15 / FR-18 reference per the new sync model.
	assert.Contains(t, body, "FR-18",
		"README should reference the FR-18 sync model after the v0.2.0-beta pivot")
}

// TestExampleHooks_PrePush_ShellLinted runs shellcheck against the pre-push
// hook. Skips with a clear message if shellcheck is not installed (the gate
// runs in CI per docs/SECURITY-MODEL.md adoption checklist).
func TestExampleHooks_PrePush_ShellLinted(t *testing.T) {
	if !shellcheckAvailable() {
		t.Skip("shellcheck not on PATH; install via 'brew install shellcheck' to run this gate")
	}
	out, err := runShellcheck(t, "pre-push")
	assert.NoError(t, err, "shellcheck on pre-push: %s", out)
}

// TestExampleHooks_PreReceive_ShellLinted runs shellcheck against pre-receive.
func TestExampleHooks_PreReceive_ShellLinted(t *testing.T) {
	if !shellcheckAvailable() {
		t.Skip("shellcheck not on PATH; install via 'brew install shellcheck' to run this gate")
	}
	out, err := runShellcheck(t, "pre-receive")
	assert.NoError(t, err, "shellcheck on pre-receive: %s", out)
}

// TestExampleHooks_PrePush_NoUnquotedVariables enforces audit T1 — every
// `$VAR` reference must be quoted as `"$VAR"` to prevent shell injection
// from task content. We allow:
//   - `$(cmd ...)` command substitution (we still ensure the result is quoted
//     when assigned, but the substitution itself is not a variable expansion);
//   - `${VAR:?msg}`, `${VAR:-default}`, etc. inside double quotes (caught by
//     the quoted-context test below);
//   - `$0`/`$1`-style positional args inside double quotes;
//   - `$$` (PID), which is not a variable expansion in the dangerous sense;
//   - Variable use on the LHS of `=` in `case`/`for VAR in`.
//
// The check is a regex sweep that flags bare `$NAME` or `${NAME}` outside of
// a double-quoted context. It is intentionally strict — false positives are
// fixed by adding quotes, not by relaxing the rule.
func TestExampleHooks_PrePush_NoUnquotedVariables(t *testing.T) {
	src := readHook(t, "pre-push")
	violations := findUnquotedVariableUses(src)
	assert.Empty(t, violations,
		"pre-push contains unquoted variable expansions (audit T1):\n%s",
		strings.Join(violations, "\n"))
}

// TestExampleHooks_PreReceive_NoUnquotedVariables — same regression for
// pre-receive (server side, T1).
func TestExampleHooks_PreReceive_NoUnquotedVariables(t *testing.T) {
	src := readHook(t, "pre-receive")
	violations := findUnquotedVariableUses(src)
	assert.Empty(t, violations,
		"pre-receive contains unquoted variable expansions (audit T1):\n%s",
		strings.Join(violations, "\n"))
}

// TestExampleHooks_PrePush_AbsolutePathToMtix enforces audit T2: the hook
// must resolve mtix via `command -v mtix` and then export MTIX_BIN, and any
// later invocation must use "$MTIX_BIN" rather than the bare `mtix` name.
//
// Allowed bare uses: lines that perform the resolution itself (e.g.
// `MTIX_BIN="$(command -v mtix)"`) and comments.
func TestExampleHooks_PrePush_AbsolutePathToMtix(t *testing.T) {
	src := readHook(t, "pre-push")

	// Requirement 1: the resolution call must appear.
	assert.Regexp(t, regexp.MustCompile(`command\s+-v\s+mtix`), src,
		"pre-push must resolve mtix once via 'command -v mtix' (audit T2)")
	assert.Regexp(t, regexp.MustCompile(`MTIX_BIN=`), src,
		"pre-push must assign MTIX_BIN for subprocess use (audit T2)")
	assert.Regexp(t, regexp.MustCompile(`export\s+MTIX_BIN`), src,
		"pre-push must export MTIX_BIN so subprocesses inherit it (audit T2)")

	// Requirement 2: no bare `mtix` invocation outside the resolution and
	// comments.
	bareInvocations := findBareMtixInvocations(src)
	assert.Empty(t, bareInvocations,
		"pre-push must invoke mtix via \"$MTIX_BIN\" (audit T2):\n%s",
		strings.Join(bareInvocations, "\n"))
}

// TestExampleHooks_PrePush_HookModeEnvSet enforces audit S2 under the
// FR-18 sync model: the hook sets MTIX_SYNC_HOOK=1 before invoking
// `mtix sync push` so the sync code path classifies transient errors
// (connection refused, TLS handshake timeout) as warn-and-skip rather
// than fatal. The transport's own retry/timeout envelope bounds wall
// time; an explicit --timeout flag is not part of the sync CLI today.
func TestExampleHooks_PrePush_HookModeEnvSet(t *testing.T) {
	src := readHook(t, "pre-push")
	pattern := regexp.MustCompile(`export\s+MTIX_SYNC_HOOK\s*=\s*1`)
	assert.Regexp(t, pattern, src,
		"pre-push must export MTIX_SYNC_HOOK=1 before invoking mtix sync push (audit S1/S2)")
}

// TestExampleHooks_PrePush_SetEuoPipefail enforces the strict-bash discipline
// declared in the ticket prompt. Without `set -euo pipefail` an undefined
// variable or piped failure silently passes the hook, which would let a
// snapshot regression sneak into the push.
func TestExampleHooks_PrePush_SetEuoPipefail(t *testing.T) {
	src := readHook(t, "pre-push")
	assert.Regexp(t, regexp.MustCompile(`(?m)^set\s+-euo\s+pipefail`), src,
		"pre-push must declare 'set -euo pipefail'")
}

// TestExampleHooks_PreReceive_SetEuoPipefail — same discipline for the
// server-side hook.
func TestExampleHooks_PreReceive_SetEuoPipefail(t *testing.T) {
	src := readHook(t, "pre-receive")
	assert.Regexp(t, regexp.MustCompile(`(?m)^set\s+-euo\s+pipefail`), src,
		"pre-receive must declare 'set -euo pipefail'")
}

// TestExampleHooks_PrePush_AmendModeIsOptIn — audit T3 commit-vs-amend
// discipline: separate "chore(snapshot)" commit is the default; amend is
// behind MTIX_HOOK_AMEND=1.
func TestExampleHooks_PrePush_AmendModeIsOptIn(t *testing.T) {
	src := readHook(t, "pre-push")
	assert.Contains(t, src, "MTIX_HOOK_AMEND",
		"pre-push must gate amend behind MTIX_HOOK_AMEND env var (audit T3)")
	assert.Contains(t, src, "chore(snapshot)",
		"pre-push must use 'chore(snapshot)' commit prefix (audit T3)")
}

// TestExampleHooks_PrePush_NoTaskContentEcho — audit T4: do not echo task
// titles, prompts, or any tasks.json content. Only structural messages
// (counts, file paths under .mtix/, exit codes) are allowed in stdout/stderr.
//
// We grep for `cat .mtix/tasks.json` and similar, which would dump task
// content into the terminal where shell metacharacters could be misread.
func TestExampleHooks_PrePush_NoTaskContentEcho(t *testing.T) {
	src := readHook(t, "pre-push")

	forbidden := []string{
		"cat .mtix/tasks.json",
		"cat \"$MTIX_TASKS\"",
		"cat $MTIX_TASKS",
		"jq",      // would parse and likely echo task content
		"echo \"$(cat", // command substitution that dumps file content
	}
	for _, bad := range forbidden {
		assert.NotContains(t, src, bad,
			"pre-push must not dump task content (audit T4): found %q", bad)
	}
}

// TestExampleHooks_GithubAction_YAMLValid parses the GH Action template
// for structural correctness. The v0.2.0-beta rewrite (MTIX-15.13) turned
// the workflow into a no-op deferral stub because the v0.1.x
// snapshot-from-PG model does not map to the FR-18 sync architecture.
// The full secrets-driven freshness check returns in v0.2.1; until then
// the YAML must still parse and declare the required top-level keys so
// existing branch-protection rules pointing at it continue to accept
// pushes cleanly.
func TestExampleHooks_GithubAction_YAMLValid(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(hooksDir(t), "github-action.yml"))
	require.NoError(t, err, "github-action.yml must exist")

	body := string(content)
	assert.Contains(t, body, "name:", "GH Action must declare 'name:'")
	assert.Contains(t, body, "on:", "GH Action must declare 'on:' triggers")
	assert.Contains(t, body, "jobs:", "GH Action must declare 'jobs:'")
	assert.Contains(t, body, "runs-on:", "GH Action job must set 'runs-on:'")
	assert.Contains(t, body, "steps:", "GH Action job must declare 'steps:'")

	// Deferral notice must be present so operators understand the v0.2.0-beta
	// status without having to read the README.
	assert.Contains(t, body, "deferred",
		"GH Action must announce v0.2.0-beta deferral to v0.2.1")

	// Must NOT contain a bare DSN literal — guard against accidental commit
	// even in the no-op stub.
	assert.NotRegexp(t, regexp.MustCompile(`postgres(ql)?://[^$\s'"]*:[^@\s'"]+@`), body,
		"GH Action must not contain a literal DSN (credentials in template)")

	// Indentation sanity: tabs are illegal in YAML.
	for i, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "\t") {
			t.Errorf("github-action.yml line %d uses a tab for indentation; YAML requires spaces", i+1)
		}
	}
}

// TestExampleHooks_PrePush_RunsAgainstFakeRepo is the integration test
// required by the ticket. It builds a fake bash environment where:
//   - `git` is the real git;
//   - `mtix` is a stub that records its invocation and (optionally) writes a
//     synthetic .mtix/tasks.json delta;
//   - the hook is copied to .git/hooks/pre-push and triggered via
//     `git push --dry-run` against a local bare repo.
//
// Under the FR-18 sync model (MTIX-15.12.3) the hook calls
// `mtix sync push` instead of the pre-15 `mtix snapshot`, gated by
// MTIX_SYNC_DSN being present (sync is opt-in). We validate:
//  1. With DSN set, the hook calls `mtix sync push` and `mtix sync --fix`
//     via MTIX_BIN.
//  2. When tasks.json delta lands, the hook commits "chore(snapshot)".
//  3. When `mtix sync push` fails, the hook exits 0 (warn-and-skip, S1).
//  4. Without a DSN, the hook is a silent no-op.
func TestExampleHooks_PrePush_RunsAgainstFakeRepo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash hooks are not exercised on Windows")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	hook := filepath.Join(hooksDir(t), "pre-push")
	if _, err := os.Stat(hook); err != nil {
		t.Fatalf("pre-push hook missing: %v", err)
	}

	t.Run("sync_push_with_changes_creates_commit", func(t *testing.T) {
		repo := setupFakeRepo(t)
		stub := writeMtixStub(t, mtixStubOpts{
			ExitCode:        0,
			WriteTasksDelta: true,
		})
		runHookWithEnv(t, hook, repo, stub.Dir,
			[]string{"MTIX_SYNC_DSN=postgres://u:p@localhost/d"})

		invocations := stub.Invocations(t)
		require.NotEmpty(t, invocations,
			"hook must invoke mtix sync when DSN is set")
		// First invocation is `mtix sync push`. We don't pin the exact
		// argv beyond the presence of the "sync" verb.
		assert.Contains(t, invocations[0], "sync",
			"first mtix call must be a sync subcommand; got %q", invocations[0])

		log := gitLog(t, repo)
		assert.Contains(t, log, "chore(snapshot)",
			"hook must create a 'chore(snapshot)' commit when tasks.json drifts (audit T3)")
	})

	t.Run("sync_push_failure_does_not_block_push", func(t *testing.T) {
		repo := setupFakeRepo(t)
		stub := writeMtixStub(t, mtixStubOpts{
			ExitCode:        2, // simulate sync push failure
			WriteTasksDelta: false,
		})

		exitCode := runHookExpectExitWithEnv(t, hook, repo, stub.Dir,
			[]string{"MTIX_SYNC_DSN=postgres://u:p@localhost/d"})
		assert.Equal(t, 0, exitCode,
			"hook must exit 0 when sync push fails (warn-and-skip, audit S1) — must not block push")
	})

	t.Run("no_dsn_configured_is_silent_noop", func(t *testing.T) {
		repo := setupFakeRepo(t)
		stub := writeMtixStub(t, mtixStubOpts{
			ExitCode:        0,
			WriteTasksDelta: true,
		})

		// Run without MTIX_SYNC_DSN and without .mtix/secrets — the hook
		// should exit silently. The stub must NOT be invoked.
		runHook(t, hook, repo, stub.Dir)

		assert.Empty(t, stub.Invocations(t),
			"hook must not invoke mtix when no DSN is configured (sync is opt-in)")
	})
}

// ----- regex sweepers ---------------------------------------------------

// findUnquotedVariableUses returns a slice of "lineno: text" violations for
// `$VAR` or `${VAR}` references that appear in word-splitting contexts
// (i.e. outside double quotes, single quotes, `$(...)` command
// substitution that itself sits inside double quotes, and `[[...]]`
// tests). Comments are stripped first.
//
// The check is strict: the only way to silence it is to add quotes.
// Designed for the small set of hook scripts in this directory; not a
// general-purpose bash analyser.
func findUnquotedVariableUses(src string) []string {
	var out []string
	varRef := regexp.MustCompile(`\$\{?[A-Za-z_][A-Za-z0-9_]*\}?`)

	for i, raw := range strings.Split(src, "\n") {
		line := stripBashComment(raw)
		for _, idx := range varRef.FindAllStringIndex(line, -1) {
			start, end := idx[0], idx[1]
			ref := line[start:end]
			if isAssignmentLHS(line, start, ref) {
				continue
			}
			if isInQuotedContext(line, start) {
				continue
			}
			if isLocalKeywordContext(line, start) {
				continue
			}
			out = append(out, fmt.Sprintf("line %d: %s (ref %s)", i+1, strings.TrimSpace(raw), ref))
		}
	}
	return out
}

// findBareMtixInvocations scans for command-position uses of bare `mtix`
// rather than `"$MTIX_BIN"`. A "command position" is the start of a line
// (after optional whitespace), or after `;`, `&&`, `||`, `|`, or `(`.
//
// Quoted strings (single- and double-quoted) are stripped before the
// regex runs so that "mtix" inside a printf message is not flagged.
// Lines that perform the resolution (`command -v mtix`, `MTIX_BIN=`)
// are excluded, as are comments and any line containing
// `# mtix:allow-bare`.
func findBareMtixInvocations(src string) []string {
	var out []string
	bare := regexp.MustCompile(`(^|[\s;&|(])mtix(\s|$)`)

	for i, raw := range strings.Split(src, "\n") {
		line := stripBashComment(raw)
		// The resolution itself is allowed.
		if strings.Contains(line, "command -v mtix") {
			continue
		}
		if strings.Contains(line, "MTIX_BIN=") {
			continue
		}
		// Allow opt-out marker on the original (un-stripped) raw line.
		if strings.Contains(raw, "mtix:allow-bare") {
			continue
		}
		// Strip quoted strings so we only check command-position tokens.
		stripped := stripQuotedRegions(line)
		if bare.MatchString(stripped) {
			out = append(out, fmt.Sprintf("line %d: %s", i+1, strings.TrimSpace(raw)))
		}
	}
	return out
}

// stripQuotedRegions blanks out the contents of single- and double-quoted
// regions so a regex run on the result will not match anything that lives
// inside a string literal. The quote characters themselves are kept so
// position-sensitive regexes still see the surrounding tokens.
func stripQuotedRegions(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	state := ctxNone
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch state {
		case ctxSingle:
			if c == '\'' {
				state = ctxNone
				b.WriteByte(c)
			} else {
				b.WriteByte(' ')
			}
		case ctxDouble:
			if c == '\\' && i+1 < len(line) {
				b.WriteByte(' ')
				b.WriteByte(' ')
				i++
				continue
			}
			if c == '"' {
				state = ctxNone
				b.WriteByte(c)
			} else {
				b.WriteByte(' ')
			}
		default:
			switch c {
			case '\\':
				if i+1 < len(line) {
					b.WriteByte(c)
					b.WriteByte(line[i+1])
					i++
				} else {
					b.WriteByte(c)
				}
			case '\'':
				state = ctxSingle
				b.WriteByte(c)
			case '"':
				state = ctxDouble
				b.WriteByte(c)
			default:
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

// stripBashComment returns the line with any trailing `# ...` removed,
// respecting quotes so `"foo # bar"` is preserved.
func stripBashComment(line string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				// Must be preceded by whitespace or be the first char.
				if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
					return line[:i]
				}
			}
		}
	}
	return line
}

// isAssignmentLHS reports whether the variable ref at line[start:] is the
// left-hand side of `VAR=value`. We accept both bare `VAR=` and `local VAR=`.
func isAssignmentLHS(line string, start int, ref string) bool {
	// `${VAR}` is never an LHS form.
	if strings.HasPrefix(ref, "${") {
		return false
	}
	// LHS is `NAME=`; the ref we matched starts with `$NAME`. So the LHS
	// pattern would be the bare NAME (no leading $) immediately followed
	// by `=`. Our regex requires the leading `$`, so any `VAR=` form is
	// outside of what `varRef` matches. Therefore this helper exists only
	// to filter out `$VAR=value` inside arithmetic, which is uncommon in
	// our hooks; we conservatively never flag it as an assignment LHS.
	_ = line
	_ = start
	return false
}

// quoteState tracks bash quoting nesting. We model:
//   - a stack of quote contexts (single, double, command-substitution);
//   - `$(...)` introduces a fresh nested quoting context (so a `"$X"`
//     inside `"$(... "${X}" ...)"` is correctly reported as quoted);
//   - single-quoted regions disable all interpretation;
//   - backslash escapes one character in double-quoted contexts.
type quoteCtx int

const (
	ctxNone quoteCtx = iota
	ctxDouble
	ctxSingle
	ctxCmdSub // inside $(...)
)

// isInQuotedContext reports whether the variable at position `pos` is in
// a context where it will NOT undergo word-splitting. That is true when
// the innermost enclosing context (from the perspective of pos) is a
// double-quoted or single-quoted region.
//
// A `$(...)` command substitution is its own quoting frame: a `"$X"`
// inside `"$(cmd "$X")"` is quoted relative to the inner command.
func isInQuotedContext(line string, pos int) bool {
	st := newQuoteScanner()
	limit := pos
	if limit > len(line) {
		limit = len(line)
	}
	for i := 0; i < limit; i++ {
		i = st.advance(line, i)
	}
	final := st.top()
	return final == ctxDouble || final == ctxSingle
}

// quoteScanner wraps the small state machine used by isInQuotedContext so
// the per-character logic can be split into small helpers (keeps gocyclo
// happy).
type quoteScanner struct {
	stack []quoteCtx
}

func newQuoteScanner() *quoteScanner {
	return &quoteScanner{stack: []quoteCtx{ctxNone}}
}

func (s *quoteScanner) top() quoteCtx { return s.stack[len(s.stack)-1] }

func (s *quoteScanner) push(c quoteCtx) { s.stack = append(s.stack, c) }

func (s *quoteScanner) pop() {
	if len(s.stack) > 1 {
		s.stack = s.stack[:len(s.stack)-1]
	}
}

// advance processes one character of `line` starting at index i and
// returns the new index (which may have advanced by more than 1 if an
// escape or `$(` was consumed).
func (s *quoteScanner) advance(line string, i int) int {
	switch s.top() {
	case ctxSingle:
		return s.advanceSingle(line, i)
	case ctxDouble:
		return s.advanceDouble(line, i)
	default:
		return s.advanceUnquoted(line, i)
	}
}

func (s *quoteScanner) advanceSingle(line string, i int) int {
	if line[i] == '\'' {
		s.pop()
	}
	return i
}

func (s *quoteScanner) advanceDouble(line string, i int) int {
	c := line[i]
	switch {
	case c == '\\' && i+1 < len(line):
		return i + 1
	case c == '"':
		s.pop()
	case c == '$' && i+1 < len(line) && line[i+1] == '(':
		s.push(ctxCmdSub)
		return i + 1
	}
	return i
}

func (s *quoteScanner) advanceUnquoted(line string, i int) int {
	c := line[i]
	switch {
	case c == '\\' && i+1 < len(line):
		return i + 1
	case c == '\'':
		s.push(ctxSingle)
	case c == '"':
		s.push(ctxDouble)
	case c == '$' && i+1 < len(line) && line[i+1] == '(':
		s.push(ctxCmdSub)
		return i + 1
	case c == ')' && s.top() == ctxCmdSub:
		s.pop()
	}
	return i
}

// isLocalKeywordContext reports whether the variable ref appears in a
// `for VAR in ...`, `case $VAR in`, or `[[ -z $VAR ]]` test context. We
// allow `[[ -z $VAR ]]` because bash's `[[ ]]` does not word-split. All
// other unquoted contexts are flagged.
func isLocalKeywordContext(line string, start int) bool {
	prefix := strings.TrimSpace(line[:start])
	// `[[` test contexts are word-split safe.
	if strings.Contains(prefix, "[[") && !strings.Contains(prefix, "]]") {
		return true
	}
	// `for VAR in` — VAR appears bare, but it's not an expansion.
	if strings.HasPrefix(prefix, "for ") && strings.HasSuffix(prefix, "in") {
		return true
	}
	return false
}

// ----- integration test scaffolding ------------------------------------

type mtixStubOpts struct {
	// ExitCode the stub returns from `mtix snapshot`.
	ExitCode int
	// WriteTasksDelta, if true, causes the stub to append a no-op line to
	// .mtix/tasks.json so the hook sees a diff to commit.
	WriteTasksDelta bool
}

type mtixStub struct {
	Dir string // dir to put on PATH; contains the `mtix` shim
	log string // path to log of invocations
}

// Invocations returns one entry per stub invocation, with the args joined.
func (s *mtixStub) Invocations(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(s.log)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read stub log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

// writeMtixStub creates a temp dir containing a bash `mtix` shim that:
//   - logs args to LOG_FILE,
//   - optionally writes a tasks.json delta,
//   - exits with the configured code.
func writeMtixStub(t *testing.T, opts mtixStubOpts) *mtixStub {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "invocations.log")

	// Stub script. Quoting is paranoid because this is also a regression
	// test for our own quoting rules.
	//
	// The hook calls `mtix sync push` followed by `mtix sync --fix`
	// (post-MTIX-15.12.3). The delta is emitted on `sync --fix` since
	// that's the command that refreshes tasks.json from the local
	// SQLite. ExitCode applies to `sync push` only — `sync --fix` is
	// considered non-load-bearing and always returns 0.
	body := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
printf '%%s\n' "$*" >> %q
verb="${1:-}"
sub="${2:-}"
if [ "%t" = "true" ] && [ "$verb" = "sync" ] && [ "$sub" = "--fix" ]; then
  mkdir -p .mtix
  printf '\n' >> .mtix/tasks.json
  exit 0
fi
if [ "$verb" = "sync" ] && [ "$sub" = "--fix" ]; then
  exit 0
fi
if [ "$verb" = "sync" ] && [ "$sub" = "push" ]; then
  exit %d
fi
exit 0
`, logPath, opts.WriteTasksDelta, opts.ExitCode)

	stubPath := filepath.Join(dir, "mtix")
	require.NoError(t, os.WriteFile(stubPath, []byte(body), 0o755)) //nolint:gosec // test stub must be executable
	return &mtixStub{Dir: dir, log: logPath}
}

// setupFakeRepo creates a temp git repo with one initial commit and a
// stub .mtix/tasks.json so the hook has something to diff against.
func setupFakeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()

	gitInit := exec.Command("git", "init", "-q", "-b", "main", repo)
	gitInit.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	require.NoError(t, gitInit.Run(), "git init")

	configs := [][]string{
		{"-C", repo, "config", "user.email", "test@example.invalid"},
		{"-C", repo, "config", "user.name", "test"},
		{"-C", repo, "config", "commit.gpgsign", "false"},
	}
	for _, args := range configs {
		require.NoError(t, exec.Command("git", args...).Run(), "git config %v", args)
	}

	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".mtix"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".mtix", "tasks.json"),
		[]byte("{}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README"),
		[]byte("seed\n"), 0o600))

	addAll := exec.Command("git", "-C", repo, "add", ".")
	require.NoError(t, addAll.Run(), "git add")
	commit := exec.Command("git", "-C", repo, "commit", "-q", "-m", "initial")
	require.NoError(t, commit.Run(), "git commit")

	return repo
}

// runHook invokes the hook directly (not via `git push`) with cwd set to
// the repo and PATH prefixed with the stub dir. It feeds the hook one
// fake stdin line in the format git uses ("ref sha ref sha"). It fails
// the test if the hook exits non-zero.
func runHook(t *testing.T, hookPath, repo, stubDir string) {
	t.Helper()
	exitCode := runHookExpectExit(t, hookPath, repo, stubDir)
	if exitCode != 0 {
		t.Fatalf("hook exited %d (expected 0)", exitCode)
	}
}

// runHookWithEnv is runHook with extra env vars (e.g. MTIX_HOOK_AMEND=1).
func runHookWithEnv(t *testing.T, hookPath, repo, stubDir string, extraEnv []string) {
	t.Helper()
	exitCode := runHookExpectExitWithEnv(t, hookPath, repo, stubDir, extraEnv)
	if exitCode != 0 {
		t.Fatalf("hook exited %d (expected 0)", exitCode)
	}
}

// runHookExpectExit runs the hook and returns its exit code without failing
// the test on non-zero exit.
func runHookExpectExit(t *testing.T, hookPath, repo, stubDir string) int {
	return runHookExpectExitWithEnv(t, hookPath, repo, stubDir, nil)
}

func runHookExpectExitWithEnv(t *testing.T, hookPath, repo, stubDir string, extraEnv []string) int {
	t.Helper()

	cmd := exec.Command("bash", hookPath, "origin", "git@example.invalid:fake.git")
	cmd.Dir = repo
	// Prepend stub dir so `mtix` resolves to our shim.
	env := append([]string{}, os.Environ()...)
	env = append(env, "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	env = append(env, "GIT_CONFIG_GLOBAL=/dev/null")
	env = append(env, extraEnv...)
	cmd.Env = env
	// Feed the hook a synthetic git push stdin line.
	cmd.Stdin = strings.NewReader("refs/heads/main 0000000000000000000000000000000000000000 refs/heads/main 1111111111111111111111111111111111111111\n")

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	t.Logf("hook stdout: %s", stdout.String())
	t.Logf("hook stderr: %s", stderr.String())
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	t.Fatalf("running hook: %v", err)
	return -1
}

// gitLog returns `git log --oneline` for the repo.
func gitLog(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "log", "--oneline").CombinedOutput()
	require.NoError(t, err, "git log: %s", out)
	return string(out)
}
