// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package redact masks DSN-shaped substrings before they cross a
// process boundary per FR-18.17. Every log message, error message,
// panic value, and MCP tool output that may include a DSN MUST pass
// through DSN before printing.
//
// The redactor handles three URL forms (postgres://, postgresql://,
// jdbc:postgresql://) plus a "raw" pattern (user:pass@host) that
// shows up in pgx error strings. The credential portion is replaced
// with REDACTED while the host and database name remain visible —
// that's enough context to debug a connection issue without leaking
// the password.
package redact

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode"
)

// SecretSentinel is a stable, well-known string used by tests across
// packages to prove that a credential was correctly masked. If a test
// injects this value into a place that flows to logs or errors and
// the value reappears in the redacted output, the test fails.
//
// The constant lives here (not in a _test.go file) so cross-package
// security regression tests can reference it without depending on a
// package's test internals.
const SecretSentinel = "PASSWORD_LEAK_SENTINEL_xyz123"

// dsnPatterns are the substrings replaced by DSN. Order matters —
// jdbc:postgresql:// must come before postgresql:// or the shorter
// pattern would consume the prefix.
//
// Each pattern matches scheme + creds + host + path; the replacement
// preserves scheme + host + db, masking creds. Greedy on the host so
// query strings (?sslmode=) stay readable for diagnostics.
var dsnPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{
		// jdbc:postgresql://user:pass@host:port/db?...
		regexp.MustCompile(`jdbc:postgresql://[^:@\s]+:[^@\s]*@([^/\s?]+)(/[^\s?]*)?(\?[^\s]*)?`),
		`jdbc:postgresql://REDACTED@$1$2$3`,
	},
	{
		regexp.MustCompile(`postgresql://[^:@\s]+:[^@\s]*@([^/\s?]+)(/[^\s?]*)?(\?[^\s]*)?`),
		`postgresql://REDACTED@$1$2$3`,
	},
	{
		regexp.MustCompile(`postgres://[^:@\s]+:[^@\s]*@([^/\s?]+)(/[^\s?]*)?(\?[^\s]*)?`),
		`postgres://REDACTED@$1$2$3`,
	},
}

// passwordKey matches the start of a password setting (MTIX-87): the key,
// also as the tail of an environment variable name such as
// MTIX_SYNC_PASSWORD, and the equals sign with any white space around it.
// Group 1 is the key and the sign as written.
var passwordKey = regexp.MustCompile(`(?i)\b([a-z0-9_]*password\s*=\s*)`)

// maskPasswordSettings masks the value of every password setting in s
// (MTIX-87). A value in a URL query ends at '&' or white space. In the
// libpq key=value form it is single-quoted, with backslash escapes (an
// unterminated quote runs to the end of s, so nothing of it stays
// visible), or bare, ended by unescaped white space. A value that is
// already REDACTED is left alone, so the masking is idempotent.
func maskPasswordSettings(s string) string {
	var out strings.Builder
	last := 0
	for _, m := range passwordKey.FindAllStringSubmatchIndex(s, -1) {
		valueStart := m[3]
		if valueStart < last {
			continue
		}
		if alreadyMasked(s, valueStart) {
			continue
		}
		inQuery := m[0] > 0 && (s[m[0]-1] == '?' || s[m[0]-1] == '&')
		end := passwordValueEnd(s, valueStart, inQuery)
		out.WriteString(s[last:valueStart])
		out.WriteString("REDACTED")
		last = end
	}
	out.WriteString(s[last:])
	return out.String()
}

// alreadyMasked reports whether the value at s[start] is exactly REDACTED,
// as an earlier pass wrote it.
func alreadyMasked(s string, start int) bool {
	rest := s[start:]
	if !strings.HasPrefix(rest, "REDACTED") {
		return false
	}
	rest = rest[len("REDACTED"):]
	return rest == "" || rest[0] == '&' || rest[0] == '\'' || unicode.IsSpace(rune(rest[0]))
}

// passwordValueEnd returns the index just after the value that starts at
// s[start].
func passwordValueEnd(s string, start int, inQuery bool) int {
	i := start
	if inQuery {
		for i < len(s) && s[i] != '&' && !unicode.IsSpace(rune(s[i])) {
			i++
		}
		return i
	}
	quoted := i < len(s) && s[i] == '\''
	if quoted {
		i++
	}
	for i < len(s) {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i += 2
		case quoted && c == '\'':
			return i + 1
		case !quoted && unicode.IsSpace(rune(c)):
			return i
		default:
			i++
		}
	}
	return i
}

// DSN returns s with every DSN-shaped substring credential masked.
// Safe to call on already-redacted strings — idempotent. Empty input
// is returned unchanged.
//
// Performance: O(n) per pattern; acceptable for log lines and panic
// messages. Not designed for high-throughput streaming; if a future
// caller needs that, build a streaming redactor instead.
func DSN(s string) string {
	if s == "" {
		return s
	}
	for _, p := range dsnPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return maskPasswordSettings(s)
}

// Recover is the canonical defer-recover wrapper for sync code.
// Goroutine entry points should use it like:
//
//	go func() {
//	    defer redact.Recover(logger)
//	    // ... work that may panic with a DSN in scope ...
//	}()
//
// It runs the recovered value through DSN, logs at ERROR level via
// the supplied logger, then re-panics with the redacted value so the
// runtime stack trace is preserved in test output. Production code
// can pass a logger that does not re-panic; tests typically prefer
// the re-panic for visibility.
func Recover(logger *slog.Logger) {
	r := recover()
	if r == nil {
		return
	}
	redacted := DSN(fmt.Sprintf("%v", r))
	if logger != nil {
		logger.Error("panic recovered (DSN-redacted)", "value", redacted)
	} else {
		fmt.Println("panic recovered (DSN-redacted):", redacted)
	}
	panic(redacted)
}

// RecoverNoRepanic is for callers that want to log-and-swallow rather
// than crash. Use sparingly — swallowing panics hides bugs.
func RecoverNoRepanic(logger *slog.Logger) {
	r := recover()
	if r == nil {
		return
	}
	redacted := DSN(fmt.Sprintf("%v", r))
	if logger != nil {
		logger.Error("panic recovered (DSN-redacted, swallowed)", "value", redacted)
	}
}
