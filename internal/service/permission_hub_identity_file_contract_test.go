// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func TestObserveHub_EmptyIdentityRejectsMalformedAndMultipleCommittedIDs(t *testing.T) {
	for _, contents := range []string{"malformed-synthetic-id\n", "11111111-1111-4111-8111-111111111111\n22222222-2222-4222-8222-222222222222\n"} {
		t.Run(contents[:8], func(t *testing.T) {
			f := permissionHeldHubFixture(t)
			f.board.ID = ""
			mtix := filepath.Join(f.board.Checkout, ".mtix")
			require.NoError(t, os.Mkdir(mtix, 0700))
			path := filepath.Join(mtix, "board-id")
			require.NoError(t, os.WriteFile(path, []byte(contents), 0644))
			require.ErrorIs(t, f.svc.ObserveHub(t.Context(), f.board), model.ErrOperatorStateUnreadable)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, contents, string(after))
			rows, err := f.state.RecentAlerts(t.Context(), 0)
			require.NoError(t, err)
			require.Empty(t, rows)
			dir, err := f.state.Path()
			require.NoError(t, err)
			bindings, err := filepath.Glob(filepath.Join(dir, "wake-hub-binding-*.json"))
			require.NoError(t, err)
			require.Empty(t, bindings)
		})
	}
}
