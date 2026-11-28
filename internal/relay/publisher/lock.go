// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package publisher

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
)

// ErrPublishBusy reports that another publisher holds the per-store
// publish lock (FR-21 §6.2a item 7). It is not a conflict and not a
// failure of the store: the holder is publishing, and the next tick
// carries on.
var ErrPublishBusy = errors.New("relay publish lock is held by another publisher (the daemon or another mtix command); nothing was published, retry on the next tick")

// PublishLock takes the per-store publish lock without blocking and
// returns its release. A holder elsewhere is reported as ErrPublishBusy.
type PublishLock func() (release func() error, err error)

const (
	// defaultLockWait bounds how long an operator-facing entry point
	// waits for the holder to finish a batch.
	defaultLockWait = 5 * time.Second
	lockPollEvery   = 20 * time.Millisecond
)

// processLocks backs publishers built without a PublishLock: one mutex
// per journal, so even then two publishers on one store never overlap.
var processLocks sync.Map

func processLockFor(j Journal) *sync.Mutex {
	key := any(j)
	if !reflect.TypeOf(j).Comparable() {
		key = reflect.TypeOf(j)
	}
	m, _ := processLocks.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex) //nolint:errcheck // only *sync.Mutex is stored
}

// lockStore takes the publish lock, waiting up to wait for a holder to
// finish. It never returns success without the lock.
func (p *Publisher) lockStore(ctx context.Context, wait time.Duration) (func(), error) {
	take := p.cfg.Lock
	if take == nil {
		m := processLockFor(p.cfg.Journal)
		take = func() (func() error, error) {
			if !m.TryLock() {
				return nil, ErrPublishBusy
			}
			return func() error { m.Unlock(); return nil }, nil
		}
	}
	deadline := time.Now().Add(wait)
	for {
		release, err := take()
		if err == nil {
			return func() {
				if relErr := release(); relErr != nil {
					p.log.Warn("relay publish lock release failed", "error", relErr)
				}
			}, nil
		}
		if !errors.Is(err, ErrPublishBusy) {
			return nil, fmt.Errorf("take the relay publish lock: %w", err)
		}
		if !time.Now().Before(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for the relay publish lock: %w", ctx.Err())
		case <-time.After(lockPollEvery):
		}
	}
}

// waitFor is the configured bounded wait.
func (p *Publisher) waitFor() time.Duration {
	if p.cfg.LockWait > 0 {
		return p.cfg.LockWait
	}
	return defaultLockWait
}
