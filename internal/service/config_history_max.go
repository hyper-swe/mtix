// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// HistoryMaxEntries reports the history.max_entries_per_task limit
// (MTIX-95.31.19.4, ADR-006 A3): the most change entries a task keeps before
// the oldest are trimmed. Zero means unset, the default: nothing is trimmed.
// It reads the value this process loaded (and any `mtix config set` it made),
// never the file: the store asks on every history write, which must not parse
// config.yaml inside a write transaction. A long-running process picks up an
// outside edit when it restarts. An unreadable value counts as unset.
func (cs *ConfigService) HistoryMaxEntries() int {
	raw, _ := cs.Get("history.max_entries_per_task")
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// validateHistoryMaxEntries accepts a positive whole number, or the empty
// string, which unsets the limit. Zero, a negative number and a non-integer
// are refused rather than read as unset (MTIX-95.31.19.4).
func validateHistoryMaxEntries(v string) error {
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return fmt.Errorf("history.max_entries_per_task must be a positive whole number, or empty to keep every entry, not %q: %w",
			v, model.ErrInvalidInput)
	}
	return nil
}
