// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"
)

// RequestIDMiddleware adds a unique X-Request-ID header to each request.
// If the client already provides one, it is preserved per FR-7.7.
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader("X-Request-ID")
		if reqID == "" {
			reqID = ulid.Make().String()
		}
		c.Set("request_id", reqID)
		c.Header("X-Request-ID", reqID)
		c.Next()
	}
}

// LoggingMiddleware logs each request with structured fields per NFR-4.2.
func LoggingMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		logger.Info("http request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", c.GetString("request_id"),
			"client_ip", c.ClientIP(),
		)
	}
}

// SecurityHeadersMiddleware adds defense-in-depth response headers:
// X-Frame-Options prevents clickjacking, X-Content-Type-Options prevents
// MIME sniffing, and Referrer-Policy prevents URL leakage.
func SecurityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "same-origin")
		c.Next()
	}
}

// CacheControlMiddleware adds Cache-Control: no-store and Pragma: no-cache
// to all responses per NFR-5.6. Prevents sensitive data from leaking
// via browser caches, proxy caches, or CDN caches.
func CacheControlMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		c.Next()
	}
}

// CSRFMiddleware enforces X-Requested-With: mtix header on mutation requests
// per NFR-5.5. GET, HEAD, and OPTIONS are exempt.
func CSRFMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			c.Next()
			return
		}

		// All mutation requests must include the CSRF header.
		if c.GetHeader("X-Requested-With") != "mtix" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"code":    "CSRF_VIOLATION",
					"message": "X-Requested-With: mtix header required for mutations",
				},
			})
			return
		}
		c.Next()
	}
}

// CORSMiddleware applies the browser origin rule for the web UI (FR-9.1,
// MTIX-95.14). A request without an Origin header comes from a
// non-browser client and passes. A request whose Origin allowedOrigin
// accepts (http or https on localhost or a loopback IP, any port) gets
// that origin in Access-Control-Allow-Origin. Any other Origin is refused
// with 403 and code ORIGIN_NOT_ALLOWED, preflight included.
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if !allowedOrigin(origin) {
			c.AbortWithStatusJSON(http.StatusForbidden, ErrorResponse{Error: ErrorDetail{
				Code:    "ORIGIN_NOT_ALLOWED",
				Message: "browser requests are accepted only from local origins",
			}})
			return
		}
		if origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, X-Requested-With, X-Agent-ID, X-Request-ID")
		c.Header("Access-Control-Expose-Headers", "X-Request-ID")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// HostAllowlistMiddleware refuses a request whose Host header does not
// name this server (MTIX-95.14). localhost and loopback IPs are accepted
// on any port; a server bound to a non-loopback address also accepts
// exactly its bind host (allowedHost). A refused request gets 403 with
// code HOST_NOT_ALLOWED.
func HostAllowlistMiddleware(bind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !allowedHost(c.Request.Host, bind) {
			c.AbortWithStatusJSON(http.StatusForbidden, ErrorResponse{Error: ErrorDetail{
				Code:    "HOST_NOT_ALLOWED",
				Message: "the request host is not accepted by this server",
			}})
			return
		}
		c.Next()
	}
}

// RecoveryMiddleware turns a handler panic into a 500 response with code
// INTERNAL_ERROR (MTIX-95.14). It logs the panic value, method, path,
// request id and stack, and no request headers.
func RecoveryMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			logger.Error("panic recovered",
				"panic", fmt.Sprint(rec),
				"method", c.Request.Method,
				"path", c.Request.URL.Path,
				"request_id", c.GetString("request_id"),
				"stack", string(debug.Stack()),
			)
			c.AbortWithStatusJSON(http.StatusInternalServerError, ErrorResponse{Error: ErrorDetail{
				Code:    "INTERNAL_ERROR",
				Message: "an internal error occurred",
			}})
		}()
		c.Next()
	}
}

// RateLimitMiddleware implements rate limiting per client per NFR-1.5: a
// token bucket of ratePerSec requests per second for each TCP peer
// address, keeping at most maxKeys buckets in a least-recently-used
// list, timed by the injected clock (MTIX-95.14). The key is the TCP
// peer, never a request header, so a client cannot choose its bucket.
// Returns 429 with a Retry-After header when the limit is exceeded.
func RateLimitMiddleware(ratePerSec, maxKeys int, clock func() time.Time) gin.HandlerFunc {
	limiter := newRateLimiter(ratePerSec, maxKeys, clock)
	retryAfter := fmt.Sprintf("%.1f", 1.0/float64(ratePerSec))

	return func(c *gin.Context) {
		if !limiter.allow(c.RemoteIP()) {
			c.Header("Retry-After", retryAfter)
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{
					"code":    "RATE_LIMITED",
					"message": "rate limit exceeded",
				},
			})
			return
		}
		c.Next()
	}
}
