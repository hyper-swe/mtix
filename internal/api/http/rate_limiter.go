// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"container/list"
	"math"
	"sync"
	"time"
)

// rateLimitMaxKeys bounds how many client buckets the server's rate
// limiter keeps (MTIX-95.14).
const rateLimitMaxKeys = 4096

// rateBucket is one client's token bucket (NFR-1.5).
type rateBucket struct {
	key        string
	tokens     float64
	lastRefill time.Time
	elem       *list.Element // this bucket's place in rateLimiter.order
}

// rateLimiter is a token-bucket rate limiter per key that keeps at most
// maxKeys buckets (NFR-1.5, MTIX-95.14). When a new key would exceed
// maxKeys, the least recently used bucket is evicted, so memory stays
// bounded whatever keys arrive. It is safe for concurrent use.
type rateLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second, and the bucket capacity
	maxKeys int
	clock   func() time.Time
	order   *list.List             // buckets, most recently used first
	buckets map[string]*rateBucket // key -> bucket
}

// newRateLimiter returns a limiter that allows ratePerSec requests per
// second per key, bursting up to ratePerSec, and keeps at most maxKeys
// buckets (at least one), timed by clock (MTIX-95.14).
func newRateLimiter(ratePerSec, maxKeys int, clock func() time.Time) *rateLimiter {
	return &rateLimiter{
		rate:    float64(ratePerSec),
		maxKeys: max(maxKeys, 1),
		clock:   clock,
		order:   list.New(),
		buckets: make(map[string]*rateBucket),
	}
}

// allow takes one token from key's bucket after refilling it for the time
// elapsed since its last use, and reports whether a token was available
// (NFR-1.5, MTIX-95.14).
func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock()
	b := l.bucket(key, now)
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens = math.Min(l.rate, b.tokens+elapsed*l.rate)
	b.lastRefill = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// bucket returns key's bucket and marks it most recently used. A key
// without one gets a full bucket; at capacity the least recently used
// bucket is evicted first (MTIX-95.14). The caller holds l.mu.
func (l *rateLimiter) bucket(key string, now time.Time) *rateBucket {
	if b, ok := l.buckets[key]; ok {
		l.order.MoveToFront(b.elem)
		return b
	}
	if l.order.Len() >= l.maxKeys {
		if oldest, ok := l.order.Remove(l.order.Back()).(*rateBucket); ok {
			delete(l.buckets, oldest.key)
		}
	}
	b := &rateBucket{key: key, tokens: l.rate, lastRefill: now}
	b.elem = l.order.PushFront(b)
	l.buckets[key] = b
	return b
}
