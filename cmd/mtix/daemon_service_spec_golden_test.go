// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// goldenServiceSpecs renders buildServiceSpec for every supported OS with fixed inputs,
// with the user id normalised so the output is machine independent.
func goldenServiceSpecs(t *testing.T) []byte {
	t.Helper()
	out := map[string]*serviceSpec{}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		spec, err := buildServiceSpec(goos, "/opt/mtix/bin/mtix", "/work/proj", "/home/u", 7)
		require.NoError(t, err)
		out[goos] = spec
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)
	return []byte(strings.ReplaceAll(string(raw), fmt.Sprintf("gui/%d", os.Getuid()), "gui/UID"))
}

// TestBuildServiceSpec_PerOSSpecs_MatchPreSplitGolden pins every field of the darwin, linux and
// windows service specs to the output captured before buildServiceSpec was split per OS
// (MTIX-275.5, a pure refactor).
func TestBuildServiceSpec_PerOSSpecs_MatchPreSplitGolden(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "service_spec_golden.json"))
	require.NoError(t, err)
	require.Equal(t, string(want), string(goldenServiceSpecs(t)))
}
