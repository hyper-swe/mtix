// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// ValidatePushEvent runs on one event the FR-18.7 validation that
// PushEvents runs on every event of a batch before it touches the hub
// (validator.Validate: the envelope grammar, the payload size and depth
// caps, the Lamport and vector-clock limits, and the FR-18.8 rule that an
// event may be stamped at most 24h after now). PushEvents still refuses a
// whole batch that holds an invalid event, as FR-18.7 requires; `mtix sync
// push` calls this first for each event, holds the invalid ones and sends
// only the rest (MTIX-95.12). now is the reference time of the FR-18.8 rule.
func ValidatePushEvent(e *model.SyncEvent, now time.Time) error {
	return validator.Validate(e, now, nil)
}
