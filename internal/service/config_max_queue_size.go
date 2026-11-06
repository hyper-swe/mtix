// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strconv"

	"github.com/hyper-swe/mtix/internal/model"
)

// The sync.max_queue_size config key (MTIX-112.17.16.16, FR-18): how many
// sync events may wait unpushed before a write that journals one is refused
// with ErrSyncQueueFull. 0, the default, is unlimited. The command layer
// copies the value into the store (SetMaxQueueSize) when it opens the
// project, because the store's emit path reads the cap from its own meta.

// maxQueueSizeKey is the config key of the queue cap.
const maxQueueSizeKey = "sync.max_queue_size"

// parseMaxQueueSize parses a sync.max_queue_size value: a non-negative integer
// that fits in 64 bits.
func parseMaxQueueSize(value string) (int64, error) {
	v, err := strconv.ParseInt(value, 10, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%s %q is not a non-negative integer (0 means unlimited): %w", maxQueueSizeKey, value, model.ErrInvalidInput)
	}
	return v, nil
}

// MaxQueueSize returns the configured sync.max_queue_size (0 is unlimited). A
// value that is not a non-negative integer (config.yaml edited by hand; Set
// refuses one) is never used: the default, unlimited, applies instead.
func (cs *ConfigService) MaxQueueSize() int64 {
	raw, err := cs.Get(maxQueueSizeKey)
	if err != nil {
		return 0
	}
	v, err := parseMaxQueueSize(raw)
	if err != nil {
		return 0
	}
	return v
}
