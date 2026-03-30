// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
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

// CORSMiddleware adds CORS headers for the web UI per FR-9.1.
// Only allows localhost origins to prevent cross-site data leakage.
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if origin == "" ||
			strings.HasPrefix(origin, "http://localhost") ||
			strings.HasPrefix(origin, "http://127.0.0.1") {
			if origin == "" {
				origin = "*"
			}
			c.Header("Access-Control-Allow-Origin", origin)
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

// rateBucket tracks per-agent request rate using token bucket algorithm.
type rateBucket struct {
	tokens    float64
	lastRefill time.Time
	mu        sync.Mutex
}

// RateLimitMiddleware implements per-agent rate limiting per NFR-1.5.
// Uses token bucket algorithm with configurable rate (requests/second).
// Agent identified by X-Agent-ID header. Returns 429 with Retry-After
// header when limit is exceeded.
func RateLimitMiddleware(ratePerSec int) gin.HandlerFunc {
	buckets := &sync.Map{}
	maxTokens := float64(ratePerSec)

	return func(c *gin.Context) {
		agentID := c.GetHeader("X-Agent-ID")
		if agentID == "" {
			agentID = c.ClientIP()
		}

		// Get or create bucket for this agent.
		val, _ := buckets.LoadOrStore(agentID, &rateBucket{
			tokens:    maxTokens,
			lastRefill: time.Now(),
		})
		bucket, ok := val.(*rateBucket)
		if !ok {
			c.Next()
			return
		}

		bucket.mu.Lock()
		defer bucket.mu.Unlock()

		// Refill tokens based on elapsed time.
		now := time.Now()
		elapsed := now.Sub(bucket.lastRefill).Seconds()
		bucket.tokens += elapsed * float64(ratePerSec)
		if bucket.tokens > maxTokens {
			bucket.tokens = maxTokens
		}
		bucket.lastRefill = now

		// Check if tokens available.
		if bucket.tokens < 1 {
			retryAfter := 1.0 / float64(ratePerSec)
			c.Header("Retry-After", fmt.Sprintf("%.1f", retryAfter))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{
					"code":    "RATE_LIMITED",
					"message": "rate limit exceeded",
				},
			})
			return
		}

		bucket.tokens--
		c.Next()
	}
}
