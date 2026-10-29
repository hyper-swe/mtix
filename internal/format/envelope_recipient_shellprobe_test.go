// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package format

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShellHonoursSyntaxOnly_Shells_ReportsSupport(t *testing.T) {
	dir := t.TempDir()
	ignoring := filepath.Join(dir, "sh-ignores-n")
	// Mimics an older dash that drops -n when combined with -c.
	require.NoError(t, os.WriteFile(ignoring, []byte("#!/bin/sh\nshift 2\nexec /bin/sh -c \"$1\"\n"), 0o700))

	tests := []struct {
		name  string
		shell string
		want  bool
	}{
		{"shell that ignores -n", ignoring, false},
		{"missing shell", filepath.Join(dir, "absent"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, shellHonoursSyntaxOnly(t, tc.shell))
		})
	}
}
