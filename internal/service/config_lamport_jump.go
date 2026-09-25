// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strconv"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// The sync.max_lamport_jump config key (MTIX-95.11): how far above the
// local Lamport clock an event pulled from the sync hub may be stamped.
// `mtix sync pull` quarantines an event beyond it instead of applying it,
// so one extreme hub row cannot push this replica's clock to where its own
// later events fail push validation. The default, 2^32, is justified at
// validator.DefaultMaxLamportJump.

// maxLamportJumpKey is the config key of the Lamport jump bound.
const maxLamportJumpKey = "sync.max_lamport_jump"

// maxLamportJumpDefault is the default of sync.max_lamport_jump as stored
// in the config: validator.DefaultMaxLamportJump (2^32) in decimal.
const maxLamportJumpDefault = "4294967296"

// parseMaxLamportJump parses a sync.max_lamport_jump value: a positive
// integer that fits in 64 bits.
func parseMaxLamportJump(value string) (int64, error) {
	v, err := strconv.ParseInt(value, 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%s %q is not a positive integer: %w", maxLamportJumpKey, value, model.ErrInvalidInput)
	}
	return v, nil
}

// validateMaxLamportJump refuses, for Set, a sync.max_lamport_jump value
// that is not a positive integer; every other key passes.
func validateMaxLamportJump(key, value string) error {
	if key != maxLamportJumpKey {
		return nil
	}
	if _, err := parseMaxLamportJump(value); err != nil {
		return fmt.Errorf("set %s: %w", key, err)
	}
	return nil
}

// MaxLamportJump returns the configured sync.max_lamport_jump (MTIX-95.11).
// A value that is not a positive integer (config.yaml edited by hand; Set
// refuses one) is never used: the default, validator.DefaultMaxLamportJump,
// applies instead.
func (cs *ConfigService) MaxLamportJump() int64 {
	raw, err := cs.Get(maxLamportJumpKey)
	if err != nil {
		return validator.DefaultMaxLamportJump
	}
	v, err := parseMaxLamportJump(raw)
	if err != nil {
		return validator.DefaultMaxLamportJump
	}
	return v
}
