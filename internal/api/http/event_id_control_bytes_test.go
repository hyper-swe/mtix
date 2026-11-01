// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The raw-body assertion records its input and reports JSON control bytes.
package http

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

type controlByteChecks struct {
	calls int
	body  []byte
}

type controlByteReporter interface {
	Helper()
	Errorf(string, ...any)
}

type controlByteFailures struct {
	test     *testing.T
	messages []string
}

// Helper marks the reporter frame as a helper of the enclosing test.
func (r *controlByteFailures) Helper() {
	r.test.Helper()
}

// Errorf records the assertion diagnostic without failing the enclosing test.
func (r *controlByteFailures) Errorf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}

// TestAssertNoControlBytes_Bodies_EnforcesJSONWhitespacePolicy exercises the real
// assertion through a reporter that lets both success and failure be inspected.
func TestAssertNoControlBytes_Bodies_EnforcesJSONWhitespacePolicy(t *testing.T) {
	cases := []struct {
		name    string
		body    []byte
		wantErr bool
	}{
		{"nil", nil, false},
		{"empty", []byte{}, false},
		{"printable", []byte(`{"value":"safe"}`), false},
		{"json whitespace", []byte("[\t1,\n2,\r3]"), false},
		{"non-ASCII", []byte(`{"value":"é世"}`), false},
		{"space boundary", []byte{0x20}, false},
		{"DEL", []byte{0x7f}, false},
	}
	for b := byte(0); b < 0x20; b++ {
		cases = append(cases, struct {
			name    string
			body    []byte
			wantErr bool
		}{fmt.Sprintf("control-%02x", b), []byte{'a', b, 'z'}, b != '\t' && b != '\n' && b != '\r'})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reporter := &controlByteFailures{test: t}
			var checks controlByteChecks
			assertNoControlBytes(reporter, tc.body, &checks)
			assert.Equal(t, tc.wantErr, len(reporter.messages) > 0, "%v", reporter.messages)
			assert.Equal(t, 1, checks.calls)
			assert.Equal(t, tc.body, checks.body)
		})
	}
}
