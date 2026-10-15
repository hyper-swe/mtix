// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A complete successful mutation reaches both its wrapper and root post-run.
// They must publish once, and the default logger must not print export records.
func TestAutoExport_CLIWritePublishesOnceAndDefaultLogsQuiet(t *testing.T) {
	for _, level := range []string{"info", "debug"} {
		t.Run(level, func(t *testing.T) {
			saveAndResetApp(t)
			startupApprovalFixture(t, false)
			project := t.TempDir()
			t.Chdir(project)
			require.NoError(t, os.MkdirAll(".mtix", 0755))
			require.NoError(t, os.WriteFile(filepath.Join(".mtix", "config.yaml"), []byte("prefix: TEST\n"), 0644))
			root := newRootCmd()
			root.SetArgs([]string{"--log-level", level, "create", "One publication", "--prompt", "Create once", "--acceptance", "Published once"})
			stderr := captureStderr(t, func() {
				captureStdout(t, func() { require.NoError(t, root.ExecuteContext(testContext(t))) })
			})
			if level == "info" {
				require.NotContains(t, stderr, "sync_export")
				return
			}
			require.Equal(t, 1, strings.Count(stderr, "msg=sync_export_completed"), "both CLI triggers must produce one publication")
			require.NotContains(t, stderr, "level=INFO msg=sync_export_completed")
		})
	}
}
