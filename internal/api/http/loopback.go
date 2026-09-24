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

// allowedOrigin reports whether the server bound to bind:port accepts a
// request's Origin header; CORSMiddleware and the WebSocket upgrade both
// use it (FR-9.1, MTIX-95.14). An empty Origin comes from a non-browser
// client and is accepted. Any other Origin must be exactly
// scheme://host[:port] and either be http or https on a loopback host
// (isLoopbackHost), on any port, or be the server's own origin
// (isOwnOrigin).
func allowedOrigin(origin, bind, port string) bool {
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
	return isLoopbackHost(u.Hostname()) || isOwnOrigin(u, bind, port)
}

// isOwnOrigin reports whether u is the origin of pages this server serves
// when it is bound to an address that names it (bindNamesServer): scheme
// http, host equal to the bind host, and port equal to the configured
// port, where an origin without a port means 80 (MTIX-95.14).
func isOwnOrigin(u *url.URL, bind, port string) bool {
	originPort := u.Port()
	if originPort == "" {
		originPort = "80"
	}
	return u.Scheme == "http" && bindNamesServer(bind) &&
		strings.EqualFold(u.Hostname(), bind) && originPort == port
}

// allowedHost reports whether a request's Host header names this server
// (MTIX-95.14): a loopback host (isLoopbackHost) on any port, or exactly
// the bind host when the bind address names the server (bindNamesServer).
// An empty Host is refused.
func allowedHost(hostHeader, bind string) bool {
	host := (&url.URL{Host: hostHeader}).Hostname()
	return isLoopbackHost(host) || (bindNamesServer(bind) && strings.EqualFold(host, bind))
}
