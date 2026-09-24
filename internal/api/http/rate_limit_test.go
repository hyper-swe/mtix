// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stepClock is a test clock that moves only when the test advances it.
type stepClock struct{ now time.Time }

// Now returns the current test time.
func (c *stepClock) Now() time.Time { return c.now }

// rateLimitRouter mounts RateLimitMiddleware in front of a GET handler.
func rateLimitRouter(ratePerSec, maxKeys int, clock func() time.Time) *gin.Engine {
	router := setupTestRouter()
	router.Use(RateLimitMiddleware(ratePerSec, maxKeys, clock))
	router.GET("/test", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	return router
}

// sendFrom sends GET /test from the given TCP peer and returns the status.
func sendFrom(router *gin.Engine, remoteAddr string, header map[string]string) int {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = remoteAddr
	for name, value := range header {
		req.Header.Set(name, value)
	}
	router.ServeHTTP(w, req)
	return w.Code
}

// TestRateLimitMiddleware_ClientHeaders_KeyIsTCPPeer verifies that the
// limiter key is the TCP peer address: request headers cannot select a
// fresh bucket, and the source port is not part of the key (NFR-1.5,
// MTIX-95.14).
func TestRateLimitMiddleware_ClientHeaders_KeyIsTCPPeer(t *testing.T) {
	tests := []struct {
		name   string
		first  map[string]string
		second map[string]string
	}{
		{"agent id", map[string]string{"X-Agent-ID": "agent-a"}, map[string]string{"X-Agent-ID": "agent-b"}},
		{"forwarded for", map[string]string{"X-Forwarded-For": "10.0.0.5"}, map[string]string{"X-Forwarded-For": "10.0.0.6"}},
		{"real ip", map[string]string{"X-Real-IP": "10.0.0.5"}, map[string]string{"X-Real-IP": "10.0.0.6"}},
		{"no headers", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &stepClock{now: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
			router := rateLimitRouter(1, 16, clock.Now)

			assert.Equal(t, http.StatusOK, sendFrom(router, "203.0.113.7:4711", tt.first))
			assert.Equal(t, http.StatusTooManyRequests, sendFrom(router, "203.0.113.7:4712", tt.second))
			assert.Equal(t, http.StatusOK, sendFrom(router, "203.0.113.8:4711", tt.second))
			assert.Equal(t, http.StatusOK, sendFrom(router, "[2001:db8::7]:4711", tt.first))
		})
	}
}

// TestRateLimitMiddleware_ManyPeers_EvictsLeastRecentlyUsed verifies that
// the limiter holds at most maxKeys buckets and evicts the least recently
// used one: an evicted peer starts again with a full bucket, a recently
// used peer keeps its (empty) bucket (NFR-1.5, MTIX-95.14).
func TestRateLimitMiddleware_ManyPeers_EvictsLeastRecentlyUsed(t *testing.T) {
	const a, b, c = "203.0.113.1:1", "203.0.113.2:1", "203.0.113.3:1"
	tests := []struct {
		name  string
		steps []struct {
			peer string
			want int
		}
	}{
		{"oldest peer is evicted", []struct {
			peer string
			want int
		}{
			{a, http.StatusOK}, {a, http.StatusTooManyRequests},
			{b, http.StatusOK}, {c, http.StatusOK}, // c evicts a
			{a, http.StatusOK}, // a starts again with a full bucket
		}},
		{"recently used peer is kept", []struct {
			peer string
			want int
		}{
			{a, http.StatusOK}, {b, http.StatusOK},
			{a, http.StatusTooManyRequests}, // a becomes most recently used
			{c, http.StatusOK},              // c evicts b, not a
			{a, http.StatusTooManyRequests}, // a still holds its empty bucket
			{b, http.StatusOK},              // b starts again with a full bucket
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &stepClock{now: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
			router := rateLimitRouter(1, 2, clock.Now)
			for i, step := range tt.steps {
				assert.Equal(t, step.want, sendFrom(router, step.peer, nil), "step %d (%s)", i, step.peer)
			}
		})
	}
}

// TestRateLimiter_ManyKeys_HoldsAtMostMaxKeys verifies the bucket memory
// bound directly: after more distinct keys than maxKeys, the limiter
// tracks exactly maxKeys buckets (MTIX-95.14).
func TestRateLimiter_ManyKeys_HoldsAtMostMaxKeys(t *testing.T) {
	tests := []struct {
		name    string
		maxKeys int
		keys    int
		want    int
	}{
		{"below capacity", 4, 3, 3},
		{"at capacity", 4, 4, 4},
		{"above capacity", 4, 50, 4},
		{"capacity below one keeps one", 0, 5, 1},
		{"negative capacity keeps one", -3, 5, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &stepClock{now: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
			limiter := newRateLimiter(10, tt.maxKeys, clock.Now)
			for i := 0; i < tt.keys; i++ {
				require.True(t, limiter.allow(fmt.Sprintf("198.51.100.%d", i)))
			}
			assert.Equal(t, tt.want, limiter.order.Len())
			assert.Len(t, limiter.buckets, tt.want)
		})
	}
}

// TestRateLimiter_Refill_UsesInjectedClock verifies the token bucket:
// ratePerSec tokens at most, refilled in proportion to the time elapsed on
// the injected clock (NFR-1.5, MTIX-95.14).
func TestRateLimiter_Refill_UsesInjectedClock(t *testing.T) {
	clock := &stepClock{now: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	limiter := newRateLimiter(2, 4, clock.Now)
	const key = "203.0.113.7"

	assert.True(t, limiter.allow(key))
	assert.True(t, limiter.allow(key))
	assert.False(t, limiter.allow(key), "a full bucket holds ratePerSec tokens")

	clock.now = clock.now.Add(250 * time.Millisecond)
	assert.False(t, limiter.allow(key), "a quarter second refills half a token")
	clock.now = clock.now.Add(250 * time.Millisecond)
	assert.True(t, limiter.allow(key), "half a second refills one token")
	assert.False(t, limiter.allow(key))

	clock.now = clock.now.Add(time.Hour)
	assert.True(t, limiter.allow(key))
	assert.True(t, limiter.allow(key))
	assert.False(t, limiter.allow(key), "refill stops at ratePerSec tokens")
}
