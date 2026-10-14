// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Tests alert record removal against a real unlink failure.
package oplocal

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAlertPrune_UnlinkRefused_WarnsAndKeepsOccurrence(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not bind the superuser")
	}
	s := alertFixture(t)
	_, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	warnings := captureWarnings(t)
	dir := mustPath(t, s)
	s.alertStage = func(stage string) error {
		if stage == "after:clear" {
			return os.Chmod(dir, 0o500)
		}
		return nil
	}
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
	other := alertInput()
	other.Agent = "other-agent"
	_, err = s.RecordAlert(t.Context(), other, alertTime().Add(2*time.Hour))
	require.NoError(t, err)
	require.True(t, strings.Contains(warnings.String(), "operator alert cleanup failed"))
}
