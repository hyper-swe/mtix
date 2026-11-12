// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSourceFiles_AutoImportGuard_StayWithinTheFileLimit: the auto-import
// files stay within the 500-line file limit (MTIX-112.17.16.16).
func TestSourceFiles_AutoImportGuard_StayWithinTheFileLimit(t *testing.T) {
	for _, name := range []string{
		"sync_autoimport_guard.go", "sync_autoimport_refusal_message.go", "sync_import_refusal_status.go",
	} {
		raw, err := os.ReadFile(name)
		require.NoError(t, err, name)
		require.LessOrEqual(t, strings.Count(string(raw), "\n"), 500, name)
	}
}
