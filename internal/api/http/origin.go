// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"fmt"
	"net/url"
	"strings"
)

// allowedOrigin reports whether the server bound to bind:port accepts a
// request's Origin header; CORSMiddleware and the WebSocket upgrade both
// use it (FR-9.1, MTIX-95.14, MTIX-221). An empty Origin comes from a
// non-browser client and is accepted. Any other Origin must be exactly
// scheme://host[:port], with an IPv6 host in brackets, and either be the
// server's own origin (isOwnOrigin) or match an entry of extra, the
// operator's allow-list, exactly (letter case aside).
func allowedOrigin(origin, bind, port string, extra []string) bool {
	if origin == "" {
		return true
	}
	u, ok := parseOrigin(origin)
	if !ok {
		return false
	}
	for _, allowed := range extra {
		if strings.EqualFold(origin, allowed) {
			return true
		}
	}
	return isOwnOrigin(u, bind, port)
}

// parseOrigin parses an origin written exactly as a browser sends it:
// scheme http or https, then the authority only (no user info, path,
// query or fragment), with an IPv6 host in brackets.
func parseOrigin(origin string) (*url.URL, bool) {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, false
	}
	if origin != u.Scheme+"://"+u.Host {
		return nil, false
	}
	if strings.Contains(u.Hostname(), ":") && !strings.HasPrefix(u.Host, "[") {
		return nil, false
	}
	return u, true
}

// isOwnOrigin reports whether u is the origin of pages this server serves
// (MTIX-221): scheme http, port equal to the configured port (an origin
// without a port means 80), and a host that is a loopback alias
// (isLoopbackAlias) or, when the bind address names the server
// (bindNamesServer), the bind host itself.
func isOwnOrigin(u *url.URL, bind, port string) bool {
	originPort := u.Port()
	if originPort == "" {
		originPort = "80"
	}
	if u.Scheme != "http" || originPort != port {
		return false
	}
	host := u.Hostname()
	return isLoopbackAlias(host) || (bindNamesServer(bind) && strings.EqualFold(host, bind))
}

// isLoopbackAlias reports whether host is one of the names a browser uses
// for this machine's loopback interface in an origin: localhost (any
// letter case), 127.0.0.1 or ::1. Other loopback addresses, such as
// 127.0.0.2, are not aliases.
func isLoopbackAlias(host string) bool {
	return strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1"
}

// ValidateAllowedOrigins checks the operator's extra browser origins
// (MTIX-221). Each entry must be written as a browser sends it:
// http://host[:port] or https://host[:port], with no path, trailing
// slash, user info, query or fragment, an IPv6 host in brackets, and no
// port that is the scheme's default. An entry that breaks these rules
// could never match a browser origin, so it is refused.
func ValidateAllowedOrigins(origins []string) error {
	for _, origin := range origins {
		u, ok := parseOrigin(origin)
		if !ok || u.Hostname() == "" {
			return fmt.Errorf("allowed origin %q: write it as scheme://host[:port] with scheme http or https and nothing after the port", origin)
		}
		if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
			return fmt.Errorf("allowed origin %q: omit the default port, as browsers do", origin)
		}
	}
	return nil
}
