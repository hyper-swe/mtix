// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"os"
)

// PermissionAlertAccepted verifies a retained exact transition receipt without
// counting an occurrence. Previously accepted pending Alerts recover normally.
func (s *State) PermissionAlertAccepted(ctx context.Context, input Alert, occ PermissionAlertOccurrence) (bool, error) {
	if err := ValidateAlertInput(input); err != nil {
		return false, err
	}
	if err := validatePermissionAlertOccurrence(occ); err != nil {
		return false, err
	}
	accepted := false
	err := s.withAlerts(ctx, false, func(dir *os.File, host string) error {
		if err := s.recoverAlert(dir, host); err != nil {
			return err
		}
		_, present, err := readPermissionAlertReceipt(dir, host, occ, input)
		accepted = present
		return err
	})
	return accepted && err == nil, err
}
