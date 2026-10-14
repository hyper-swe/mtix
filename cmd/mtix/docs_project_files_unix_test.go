// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// Nonregular root files must be refused without attempting a blocking FIFO read.
func TestRunDocsGenerate_NonregularRootFiles_Refuses(t *testing.T) {
	for _, name := range []string{"MTIX.md", "CLAUDE.md", "AGENTS.md"} {
		for _, kind := range []string{"fifo", "directory"} {
			t.Run(name+kind, func(t *testing.T) {
				dir := projectBriefingDir(t)
				path := filepath.Join(dir, name)
				if kind == "fifo" {
					require.NoError(t, syscall.Mkfifo(path, 0600))
				} else {
					require.NoError(t, os.Mkdir(path, 0700))
				}
				require.ErrorContains(t, runDocsGenerate(false), "not a regular file")
			})
		}
	}
}
