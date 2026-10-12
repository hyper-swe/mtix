// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil"
)

func approveTestHooks(t *testing.T, dir string) {
	t.Helper()
	approvals := NewApprovals(dir, testutil.OperatorState(t), false)
	cfg, _ := Load(dir)
	for _, hook := range cfg.Hooks {
		if hook.Exec != nil {
			require.NoError(t, approvals.Approve(hook.Name, true))
		}
	}
}
