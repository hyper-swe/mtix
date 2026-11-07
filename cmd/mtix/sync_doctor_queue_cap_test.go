// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func queueCapCheck(t *testing.T) DoctorCheck {
	t.Helper()
	r := appendQueueCapCheck(context.Background(), DoctorReport{OverallPass: true}, app.store)
	require.Len(t, r.Checks, 1)
	return r.Checks[0]
}

// TestDoctor_QueueCap_WarnsAtTheCapAndTheFixClearsIt: with no cap the check
// passes; at the cap it WARNs with the config command, and running that
// command (on the next invocation) clears the WARN (MTIX-112.17.16.16).
func TestDoctor_QueueCap_WarnsAtTheCapAndTheFixClearsIt(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("One", "", "", 3, "", "", "", "", ""))
	check := queueCapCheck(t)
	require.True(t, check.Pass)
	require.False(t, check.Warn)
	require.Contains(t, check.Detail, "no cap")

	require.NoError(t, runConfigSet("sync.max_queue_size", "1"))
	require.NoError(t, initApp(&cobra.Command{Use: "test"}, ""))
	check = queueCapCheck(t)
	require.True(t, check.Warn)
	require.Contains(t, check.Detail, "1 pending of a cap of 1")

	fix := regexp.MustCompile(`mtix config set (sync\.max_queue_size) (\d+)`).FindStringSubmatch(check.Fix)
	require.NotNil(t, fix, check.Fix)
	require.NoError(t, runConfigSet(fix[1], fix[2]), "the printed fix")
	require.NoError(t, initApp(&cobra.Command{Use: "test"}, ""))
	require.False(t, queueCapCheck(t).Warn)
}

// TestDoctor_QueueCap_HandEditedInvalidValue_WarnsWithTheFix: a cap edited by
// hand to a value that is not a number is refused and the queue is
// unlimited; the doctor row says so and prints the command that sets a valid
// cap (MTIX-112.17.16.16, M44).
func TestDoctor_QueueCap_HandEditedInvalidValue_WarnsWithTheFix(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runConfigSet("sync.max_queue_size", "5"))
	cfg := filepath.Join(app.mtixDir, "config.yaml")
	raw, err := os.ReadFile(cfg)
	require.NoError(t, err)
	lines := strings.Split(string(raw), "\n")
	for i, l := range lines {
		if strings.Contains(l, "max_queue_size") {
			lines[i] = l[:strings.LastIndex(l, ":")] + ": lots" // a hand edit
		}
	}
	require.NoError(t, os.WriteFile(cfg, []byte(strings.Join(lines, "\n")), 0o600))
	require.NoError(t, initApp(&cobra.Command{Use: "test"}, ""))

	check := queueCapCheck(t)

	require.True(t, check.Pass)
	require.True(t, check.Warn)
	require.Contains(t, check.Detail, "refused")
	require.Contains(t, check.Detail, "lots")
	require.Contains(t, check.Fix, "mtix config set sync.max_queue_size <number>")
}
