// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"net"
	"net/url"
	"strings"
)

// isLoopbackHost reports whether host names this machine's loopback
// interface: exactly "localhost" (in any letter case) or an IP address in
// a loopback range, such as 127.0.0.1 or ::1. The host carries no port
// and no brackets. It decides the origin rule, the Host allowlist and the
// bind warning alike (NFR-5.2, MTIX-95.14).
func isLoopbackHost(host string) bool {
	// ParseIP returns nil for a name, and a nil IP is not loopback.
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

// allowedOrigin reports whether the server accepts a request's Origin
// header (FR-9.1, MTIX-95.14). An empty Origin comes from a non-browser
// client and is accepted. Any other Origin must be exactly
// scheme://host[:port] with scheme http or https and a loopback host
// (isLoopbackHost), on any port.
func allowedOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	// An origin is scheme and authority only: no user info, path, query
	// or fragment.
	if origin != u.Scheme+"://"+u.Host {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

// allowedHost reports whether a request's Host header names this server
// (MTIX-95.14): a loopback host (isLoopbackHost) on any port, or exactly
// the configured bind host. The bind host adds a name only when it is not
// loopback, since a loopback bind host is already accepted. An empty Host
// is refused.
func allowedHost(hostHeader, bind string) bool {
	host := (&url.URL{Host: hostHeader}).Hostname()
	if host == "" {
		return false
	}
	return isLoopbackHost(host) || strings.EqualFold(host, bind)
}
