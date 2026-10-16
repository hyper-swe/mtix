// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// WakeLeaseKey identifies board-local ownership by agent, host and user (FR-20).
type WakeLeaseKey = sqlite.WakeLeaseKey

// WakeLease preserves the existing ownership token, journal mark and JSON shape.
type WakeLease = sqlite.WakeLease

// WakeLeaseReleaseResult preserves the release, rearm and abandon outcome shape.
type WakeLeaseReleaseResult = sqlite.ReleaseResult

// WakeLeaseStore is the atomic persistence boundary consumed by lease policy.
// Ownership, contention, fencing and rearm stay inside the store transaction,
// where the store's injected clock is sampled without an ownership gap.
type WakeLeaseStore interface {
	AcquireWakeLease(context.Context, WakeLeaseKey, string, time.Duration) (WakeLease, error)
	ReleaseWakeLease(context.Context, WakeLeaseKey, string, time.Duration, bool) (WakeLeaseReleaseResult, error)
}

// WakeLeaseService owns lease request policy and explicit operation intent.
type WakeLeaseService struct{ backend WakeLeaseStore }

// NewWakeLeaseService injects only the atomic lease persistence capabilities.
func NewWakeLeaseService(backend WakeLeaseStore) *WakeLeaseService {
	return &WakeLeaseService{backend: backend}
}

// Acquire requests a fresh owner token or the existing redacted held outcome.
func (s *WakeLeaseService) Acquire(ctx context.Context, key WakeLeaseKey, ttl time.Duration) (WakeLease, error) {
	if err := validateWakeLeaseRequest(key, "", ttl); err != nil {
		return WakeLease{}, err
	}
	return s.backend.AcquireWakeLease(ctx, key, "", ttl)
}

// Renew uses a holder token with the existing atomic acquire-or-renew contract.
// Expired ownership remains eligible for takeover with a fresh token (FR-20).
func (s *WakeLeaseService) Renew(ctx context.Context, key WakeLeaseKey, holder string, ttl time.Duration) (WakeLease, error) {
	if err := validateWakeLeaseRequest(key, holder, ttl); err != nil {
		return WakeLease{}, err
	}
	if holder == "" {
		return WakeLease{}, fmt.Errorf("renew wake lease holder required: %w", model.ErrInvalidInput)
	}
	return s.backend.AcquireWakeLease(ctx, key, holder, ttl)
}

// Release relinquishes ownership or atomically rearms the same owner (FR-20).
func (s *WakeLeaseService) Release(ctx context.Context, key WakeLeaseKey, holder string, ttl time.Duration) (WakeLeaseReleaseResult, error) {
	return s.release(ctx, key, holder, ttl, false)
}

// Abandon relinquishes ownership without rearming, even if work is pending.
func (s *WakeLeaseService) Abandon(ctx context.Context, key WakeLeaseKey, holder string, ttl time.Duration) (WakeLeaseReleaseResult, error) {
	return s.release(ctx, key, holder, ttl, true)
}

func (s *WakeLeaseService) release(ctx context.Context, key WakeLeaseKey, holder string, ttl time.Duration, abandon bool) (WakeLeaseReleaseResult, error) {
	if err := validateWakeLeaseRequest(key, holder, ttl); err != nil {
		return WakeLeaseReleaseResult{}, err
	}
	if holder == "" {
		return WakeLeaseReleaseResult{}, fmt.Errorf("release wake lease holder required: %w", model.ErrInvalidInput)
	}
	return s.backend.ReleaseWakeLease(ctx, key, holder, ttl, abandon)
}
