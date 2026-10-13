// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"errors"
	"os"
	"strings"
	"time"
)

// pruneAlertRates removes rate records whose one-hour window has closed
// (MTIX-112.17.11), so operator-local alert state stays bounded by recent
// activity. An absent rate record is already a valid state for an expired
// window, so removal never changes what a later Raise or Recent accepts.
// The alert-rate- prefix test only avoids reading unrelated files; the
// name-equals-hash check below is the authoritative gate, so widening the
// prefix is an equivalent mutation. Cleanup is best effort for the caller.
// Records that cannot be read or validated are left in place: the normal
// read paths refuse them visibly and deleting unknown data is never safe.
func (s *State) pruneAlertRates(dir *os.File, host string, now time.Time) error {
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return failure("alert rate listing failed", err)
	}
	var pruneErr error
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "alert-rate-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		var rate Alert
		present, readErr := readAlertRecord(dir, name, host, &rate)
		if readErr != nil || !present || validateStoredAlert(rate, host) != nil || name != alertRateName(rate.Agent, rate.Class) {
			continue
		}
		if now.Before(rate.FirstAt.Add(time.Hour)) {
			continue
		}
		if s.alertStage != nil {
			if stageErr := s.alertStage("prune:" + name); stageErr != nil {
				pruneErr = errors.Join(pruneErr, stageErr)
				continue
			}
		}
		if removeErr := removeData(dir, name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			pruneErr = errors.Join(pruneErr, removeErr)
		}
	}
	return pruneErr
}
