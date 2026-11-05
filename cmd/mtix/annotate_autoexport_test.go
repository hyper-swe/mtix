// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-112.31: the annotate command's RunE must stay wrapped in withAutoExport,
// with and without --to. The command is run on its own (no root post-run), so a
// publication of tasks.json can only come from the wrapper.
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnnotateCmd_PublishesTasksJSONThroughAutoExportWrapper(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"without --to", nil},
		{"with --to", []string{"--to", "developer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initTestApp(t)
			require.NotNil(t, app.syncSvc)
			cwd, err := os.Getwd()
			require.NoError(t, err)
			app.mtixDir = filepath.Join(cwd, ".mtix")
			id := seedOneNode(t)
			tasksPath := filepath.Join(app.mtixDir, "tasks.json")
			_, statErr := os.Stat(tasksPath)
			require.Error(t, statErr, "no export exists before the annotate run")

			sendAnnotate(t, append([]string{id, "wrapper pinned note"}, tc.args...)...)

			data, readErr := os.ReadFile(tasksPath)
			require.NoError(t, readErr, "annotate must publish tasks.json through withAutoExport")
			assert.Contains(t, string(data), "wrapper pinned note")
		})
	}
}
