// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package postgres

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"sync"
)

// dsnPattern matches Postgres DSN-shaped strings. We use a deliberately
// permissive pattern so contributors who paste a wrapped or oddly-encoded
// DSN still trigger the redactor:
//
//	postgres://user:pass@host:5432/db?...
//	postgresql://user:pass@host/db
//	host=... user=... password=... ...
//
// The regex captures the entire token; the redactor replaces it wholesale
// with `<REDACTED:postgres-dsn>` so structure-aware grepping still works.
var dsnPattern = regexp.MustCompile(
	`postgres(?:ql)?://[^\s'"<>]+|password=[^\s'"<>;]+|user=[^\s'"<>;]+@[^\s'"<>;]+`,
)

const dsnReplacement = "<REDACTED:postgres-dsn>"

// RedactDSN returns a copy of s with every DSN-shaped token replaced by
// dsnReplacement. Safe to call on arbitrary input; never panics.
func RedactDSN(s string) string {
	if !mayContainDSN(s) {
		return s
	}
	return dsnPattern.ReplaceAllString(s, dsnReplacement)
}

// mayContainDSN is a fast-path check. Avoiding the regex engine for the
// common case (test logs that don't mention a DSN) keeps the redactor's
// overhead negligible even when called on every Write.
func mayContainDSN(s string) bool {
	return strings.Contains(s, "postgres://") ||
		strings.Contains(s, "postgresql://") ||
		strings.Contains(s, "password=")
}

// RedactingWriter wraps an io.Writer and applies RedactDSN to every chunk
// written through it. Used by the test reporter to ensure that a misbehaving
// store driver cannot leak its DSN into stderr.
//
// Concurrency: writes are serialized with a mutex. The wrapped writer must
// be safe to use from a single goroutine; the wrapper makes it usable from
// many.
type RedactingWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewRedactingWriter wraps w. Writes are passed through RedactDSN before
// reaching w.
func NewRedactingWriter(w io.Writer) *RedactingWriter {
	return &RedactingWriter{w: w}
}

// Write satisfies io.Writer. The total bytes reported is the length of the
// ORIGINAL slice (not the redacted output), so callers using the result for
// position tracking remain correct.
func (r *RedactingWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !mayContainDSN(string(p)) {
		_, err := r.w.Write(p)
		return len(p), err
	}

	redacted := dsnPattern.ReplaceAll(p, []byte(dsnReplacement))
	if _, err := r.w.Write(redacted); err != nil {
		return 0, err
	}
	return len(p), nil
}

// captureRedacted is a test helper that runs body with stdout/stderr
// captured and DSN-redacted. Returns the redacted output for assertions.
// Public for use by harness tests.
func captureRedacted(body func(io.Writer)) string {
	var buf bytes.Buffer
	w := NewRedactingWriter(&buf)
	body(w)
	return buf.String()
}
