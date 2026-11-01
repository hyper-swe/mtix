// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// wsAuthProtocol is the fixed WebSocket subprotocol the server answers with.
// A browser client offers it next to wsAuthProtocolPrefix+token, because a
// browser cannot set an Authorization header on a WebSocket. The server never
// echoes the token form back (MTIX-220).
const (
	wsAuthProtocol       = "mtix.auth"
	wsAuthProtocolPrefix = "mtix.auth."
	wsEventsPath         = "/ws/events"
)

// ErrExposureRefused is returned when a network bind lacks --insecure-bind or
// an authentication token (NFR-5.2, MTIX-220).
var ErrExposureRefused = errors.New("network bind refused")

// IsLoopbackHost reports whether host names this machine's loopback
// interface: localhost in any letter case or a loopback IP address. It is the
// same rule the Host allowlist, the origin rule and the exposure check use.
func IsLoopbackHost(host string) bool { return isLoopbackHost(host) }

// CheckExposure decides whether a server may listen on bind (NFR-5.2,
// MTIX-220). A loopback bind needs neither the insecure-bind setting nor a
// token. Any other bind needs both, and the refusal names both and prints the
// exact fix. Server.Start and the serve command both call it.
func CheckExposure(bind string, insecureBind, tokenConfigured bool) error {
	if isLoopbackHost(bind) || (insecureBind && tokenConfigured) {
		return nil
	}
	return fmt.Errorf("%w: %q is not a loopback address, so serving there exposes every task to the network. "+
		"It needs --insecure-bind (you must opt in) and a bearer token (the server creates one on first use). "+
		"Fix: run mtix serve --addr %s --insecure-bind; to stay local use --addr 127.0.0.1",
		ErrExposureRefused, bind, bind)
}

// AuthMiddleware requires "Authorization: Bearer <token>" on every request
// (MTIX-220). A missing, malformed or wrong credential gets 401 with code
// AUTH_REQUIRED and no hint about which part failed. The comparison is
// constant time over SHA-256 digests, so it does not depend on length or
// content. A CORS preflight passes: it carries no credential by design, the
// CORS middleware answers it with no data, and a disallowed origin is refused
// there. The WebSocket upgrade also accepts the mtix.auth.<token> subprotocol.
func AuthMiddleware(token string) gin.HandlerFunc {
	want := sha256.Sum256([]byte(token))
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}
		presented, ok := bearerToken(c.Request.Header.Get("Authorization"))
		if !ok && c.Request.URL.Path == wsEventsPath {
			presented, ok = subprotocolToken(c.Request.Header.Values("Sec-WebSocket-Protocol"))
		}
		if ok {
			got := sha256.Sum256([]byte(presented))
			if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
				c.Next()
				return
			}
		}
		c.Header("WWW-Authenticate", "Bearer")
		c.AbortWithStatusJSON(http.StatusUnauthorized, ErrorResponse{Error: ErrorDetail{
			Code:    "AUTH_REQUIRED",
			Message: "a valid bearer token is required",
		}})
	}
}

// bearerToken extracts the token of a "Bearer <token>" header: the scheme is
// case-insensitive and exactly one space separates it from a non-empty token.
func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

// subprotocolToken extracts the token from a Sec-WebSocket-Protocol offer
// that lists mtix.auth.<token> among its comma-separated protocols.
func subprotocolToken(values []string) (string, bool) {
	for _, value := range values {
		for _, offered := range strings.Split(value, ",") {
			offered = strings.TrimSpace(offered)
			if token, found := strings.CutPrefix(offered, wsAuthProtocolPrefix); found && token != "" {
				return token, true
			}
		}
	}
	return "", false
}
