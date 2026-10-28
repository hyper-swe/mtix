// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hostguard_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

const reducedChildEnv = "MTIX_HOSTGUARD_REDUCED_CHILD"

// TestRun_ReducedEnvironmentChild_AdoptsParentScratchRoot re-executes the test
// binary with only HOME, the shape used by helper processes that build their
// environment from scratch. The child must reuse the parent's tree, stay
// isolated, and leave removal to the parent.
func TestRun_ReducedEnvironmentChild_AdoptsParentScratchRoot(t *testing.T) {
	if os.Getenv(reducedChildEnv) == "1" {
		require.NoError(t, hostguard.Assert())
		assert.Equal(t, os.Getenv("MTIX_HOSTGUARD_EXPECT_ROOT"), os.Getenv(hostguard.RootEnv))
		return
	}
	binary, err := os.Executable()
	require.NoError(t, err)
	root := os.Getenv(hostguard.RootEnv)
	cmd := exec.Command(binary, "-test.run=^TestRun_ReducedEnvironmentChild_AdoptsParentScratchRoot$")
	cmd.Env = []string{reducedChildEnv + "=1", "MTIX_HOSTGUARD_EXPECT_ROOT=" + root, "HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	_, statErr := os.Stat(root)
	require.NoError(t, statErr, "the child must not remove the parent's scratch root")
}
