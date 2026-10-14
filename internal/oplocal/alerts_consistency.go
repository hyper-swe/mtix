// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"log/slog"
	"math"
	"os"
	"time"
)

func validateProposedAlert(row Alert, segments [3]alertSegment) error {
	for _, segment := range segments {
		for _, stored := range segment.rows {
			if stored.ID != row.ID {
				continue
			}
			merged, err := reconcileAlert(stored, row)
			if err != nil {
				return err
			}
			if merged.Count != row.Count || merged.LastAt.After(row.LastAt) {
				return invalid("proposed alert predates retained state")
			}
		}
	}
	return nil
}

func validateAcceptedAlertRate(dir *os.File, host string, row Alert) error {
	var previous Alert
	present, err := readAlertRecord(dir, alertRateName(row.Agent, row.Class), host, &previous)
	if err != nil {
		return err
	}
	if !present {
		if row.Count != 1 {
			return invalid("accepted alert rate is unavailable")
		}
		return nil
	}
	if validationErr := validateStoredAlert(previous, host); validationErr != nil {
		return validationErr
	}
	if previous.Agent != row.Agent || previous.Class != row.Class {
		return invalid("accepted alert rate key conflicts")
	}
	if previous.ID != row.ID {
		if row.Count != 1 || row.FirstAt.Before(previous.FirstAt.Add(time.Hour)) || row.FirstAt.Before(previous.LastAt) {
			return invalid("accepted alert window conflicts")
		}
		return nil
	}
	return validateAcceptedAlertTransition(previous, row)
}

func validateAcceptedAlertTransition(previous, row Alert) error {
	if _, err := reconcileAlert(previous, row); err != nil {
		return err
	}
	if row.Count != previous.Count && (previous.Count == math.MaxUint64 || row.Count != previous.Count+1) {
		return invalid("accepted alert count transition is invalid")
	}
	if previous.LastAt.After(row.LastAt) {
		return invalid("accepted alert predates rate state")
	}
	return nil
}

// warnMissingAlertRate reports a retained row whose window is still open
// although its rate record is gone (pruned, lost or a clock step back). The
// occurrence is still counted as a fresh window; the warning makes the
// restarted count visible instead of refusing the alert.
func warnMissingAlertRate(input Alert, now time.Time, segments [3]alertSegment) {
	for _, segment := range segments {
		for _, row := range segment.rows {
			if row.Agent == input.Agent && row.Class == input.Class && now.Before(row.FirstAt.Add(time.Hour)) {
				slog.Warn("operator alert rate record missing; count restarts", "class", input.Class)
				return
			}
		}
	}
}
