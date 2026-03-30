// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// setupTestRouter creates a Gin router in test mode.
func setupTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

// TestCSRFMiddleware_GET_Passes verifies GET bypasses CSRF.
func TestCSRFMiddleware_GET_Passes(t *testing.T) {
	router := setupTestRouter()
	router.Use(CSRFMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestCSRFMiddleware_POST_NoHeader_403 verifies POST without header is blocked.
func TestCSRFMiddleware_POST_NoHeader_403(t *testing.T) {
	router := setupTestRouter()
	router.Use(CSRFMiddleware())
	router.POST("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestCSRFMiddleware_POST_WithHeader_Passes verifies POST with header passes.
func TestCSRFMiddleware_POST_WithHeader_Passes(t *testing.T) {
	router := setupTestRouter()
	router.Use(CSRFMiddleware())
	router.POST("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.Header.Set("X-Requested-With", "mtix")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestCSRFMiddleware_DELETE_NoHeader_403 verifies DELETE without header is blocked.
func TestCSRFMiddleware_DELETE_NoHeader_403(t *testing.T) {
	router := setupTestRouter()
	router.Use(CSRFMiddleware())
	router.DELETE("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestCSRFMiddleware_OPTIONS_Passes verifies OPTIONS bypasses CSRF.
func TestCSRFMiddleware_OPTIONS_Passes(t *testing.T) {
	router := setupTestRouter()
	router.Use(CSRFMiddleware())
	router.OPTIONS("/test", func(c *gin.Context) {
		c.Status(204)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	router.ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusForbidden, w.Code)
}

// TestCacheControlMiddleware_SetsHeaders verifies cache headers per NFR-5.6.
func TestCacheControlMiddleware_SetsHeaders(t *testing.T) {
	router := setupTestRouter()
	router.Use(CacheControlMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", w.Header().Get("Pragma"))
}

// TestRequestIDMiddleware_GeneratesID verifies request ID is generated.
func TestRequestIDMiddleware_GeneratesID(t *testing.T) {
	router := setupTestRouter()
	router.Use(RequestIDMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	router.ServeHTTP(w, req)

	reqID := w.Header().Get("X-Request-ID")
	assert.NotEmpty(t, reqID)
	assert.Len(t, reqID, 26) // ULID length
}

// TestRequestIDMiddleware_PreservesClientID verifies client ID is preserved.
func TestRequestIDMiddleware_PreservesClientID(t *testing.T) {
	router := setupTestRouter()
	router.Use(RequestIDMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Request-ID", "my-custom-id")
	router.ServeHTTP(w, req)

	assert.Equal(t, "my-custom-id", w.Header().Get("X-Request-ID"))
}

// TestLoggingMiddleware_LogsRequest verifies request logging.
func TestLoggingMiddleware_LogsRequest(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	router := setupTestRouter()
	router.Use(LoggingMiddleware(logger))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	router.ServeHTTP(w, req)

	output := buf.String()
	assert.Contains(t, output, "http request")
	assert.Contains(t, output, "GET")
	assert.Contains(t, output, "/test")
}

// TestRateLimitMiddleware_AllowsNormalTraffic verifies normal traffic passes.
func TestRateLimitMiddleware_AllowsNormalTraffic(t *testing.T) {
	router := setupTestRouter()
	router.Use(RateLimitMiddleware(100))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestRateLimitMiddleware_ReturnsRetryAfter verifies 429 includes Retry-After.
func TestRateLimitMiddleware_ReturnsRetryAfter(t *testing.T) {
	router := setupTestRouter()
	router.Use(RateLimitMiddleware(1)) // 1 req/sec — very restrictive
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	// First request should pass.
	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.Header.Set("X-Agent-ID", "rate-test")
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Second request should be rate limited.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.Header.Set("X-Agent-ID", "rate-test")
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusTooManyRequests, w2.Code)
	assert.NotEmpty(t, w2.Header().Get("Retry-After"))
}

// TestCORSMiddleware_PreflightRequest verifies CORS preflight returns 204.
func TestCORSMiddleware_PreflightRequest(t *testing.T) {
	router := setupTestRouter()
	router.Use(CORSMiddleware())
	router.OPTIONS("/test", func(c *gin.Context) {
		// This should not be reached — CORSMiddleware handles OPTIONS.
		c.Status(200)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), "POST")
}

// TestSecurityHeadersMiddleware_SetsAllHeaders verifies defense-in-depth headers.
func TestSecurityHeadersMiddleware_SetsAllHeaders(t *testing.T) {
	router := setupTestRouter()
	router.Use(SecurityHeadersMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "same-origin", w.Header().Get("Referrer-Policy"))
}

// TestCORSMiddleware_ExternalOrigin_Rejected verifies non-localhost origins are rejected.
func TestCORSMiddleware_ExternalOrigin_Rejected(t *testing.T) {
	router := setupTestRouter()
	router.Use(CORSMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://evil.com")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}
