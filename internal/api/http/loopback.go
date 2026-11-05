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

// bindNamesServer reports whether a bind address is a name that clients
// use to reach this server: not empty and not a wildcard (unspecified)
// address such as 0.0.0.0 or ::, which listens on every interface but
// names none of them (MTIX-95.14).
func bindNamesServer(bind string) bool {
	// ParseIP returns nil for a name, and a nil IP is not unspecified.
	return bind != "" && !net.ParseIP(bind).IsUnspecified()
}

// allowedHost reports whether a request's Host header names this server
// (MTIX-95.14): a loopback host (isLoopbackHost) on any port, or exactly
// the bind host when the bind address names the server (bindNamesServer).
// An empty Host is refused.
func allowedHost(hostHeader, bind string) bool {
	host := (&url.URL{Host: hostHeader}).Hostname()
	return isLoopbackHost(host) || (bindNamesServer(bind) && strings.EqualFold(host, bind))
}
