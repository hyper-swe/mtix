// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWakeExecChild_CarriesAttemptAndHop_DropsInheritedContext(t *testing.T) {
	f := envFixture(t, "[sh, scripts/envdump.sh]")
	t.Setenv("MTIX_WAKE_HOP", "999")
	t.Setenv("MTIX_WAKE_ATTEMPT", "parent-attempt")
	hook := f.hook(t)
	plan, err := f.approvals.PrepareExec(hook)
	require.NoError(t, err)
	require.NoError(t, NewExecAdapter().Deliver(context.Background(), Delivery{Hook: hook, Exec: plan, Wake: &WakeExecContext{Attempt: "actual-attempt", Hops: []int64{1, 31, 4}}}))
	out := filepath.Join(f.root, "child.env")
	require.Eventually(t, func() bool { raw, err := os.ReadFile(out); return err == nil && strings.Contains(string(raw), "END") }, 10*time.Second, 20*time.Millisecond)
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Contains(t, string(raw), "MTIX_WAKE_HOP=31\n")
	require.Contains(t, string(raw), "MTIX_WAKE_ATTEMPT=actual-attempt\n")
	require.NotContains(t, string(raw), "parent-attempt")
	require.NotContains(t, string(raw), "MTIX_WAKE_HOP=999")
}
