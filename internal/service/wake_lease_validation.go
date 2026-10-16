// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// ParseWakeLeaseTTL parses the CLI duration while preserving its diagnostics.
// The inclusive FR-20 lease policy is one minute through twenty-four hours.
func ParseWakeLeaseTTL(value string) (time.Duration, error) {
	ttl, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid lease ttl %q (%v): %w", value, err, model.ErrInvalidInput)
	}
	if ttl < time.Minute || ttl > 24*time.Hour {
		return 0, fmt.Errorf("lease ttl must be between 1m and 24h: %w", model.ErrInvalidInput)
	}
	return ttl, nil
}

func validateWakeLeaseRequest(key WakeLeaseKey, holder string, ttl time.Duration) error {
	if key.AgentID == "" || key.HostID == "" || key.UID == "" {
		return fmt.Errorf("wake lease identity required: %w", model.ErrInvalidInput)
	}
	if ttl < time.Minute || ttl > 24*time.Hour {
		return fmt.Errorf("wake lease ttl must be between 1m and 24h: %w", model.ErrInvalidInput)
	}
	if holder != "" {
		if len(holder) != 32 {
			return fmt.Errorf("wake lease holder must be 32 lowercase hex characters: %w", model.ErrInvalidInput)
		}
		for _, c := range holder {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return fmt.Errorf("wake lease holder must be 32 lowercase hex characters: %w", model.ErrInvalidInput)
			}
		}
	}
	return nil
}
