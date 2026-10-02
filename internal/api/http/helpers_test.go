// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"time"
)

// testClock returns a fixed-time clock for deterministic tests.
func testClock() func() time.Time {
	fixed := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return fixed }
}

// localTestHost is the Host header that test requests carry: a loopback
// name that the server's Host allowlist accepts (MTIX-95.14).
const localTestHost = "127.0.0.1:8377"

// newLocalRequest is httptest.NewRequest addressed to localTestHost.
// httptest.NewRequest on its own sets the Host to example.com.
func newLocalRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = localTestHost
	return req
}
