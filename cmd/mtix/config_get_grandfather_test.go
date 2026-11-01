// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// captureStd runs fn and returns what it wrote to stdout and stderr.
func captureStd(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	errR, errW, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	fn()
	require.NoError(t, outW.Close())
	require.NoError(t, errW.Close())
	o, _ := io.ReadAll(outR)
	e, _ := io.ReadAll(errR)
	return string(o), string(e)
}

func TestConfigGet_GrandfatheredHubContact_ParseableValueAndSeparateFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("prefix: \"X\"\n"), 0o600))
	cs, err := service.NewConfigService(path)
	require.NoError(t, err)
	saved := app
	app = appContext{configSvc: cs}
	t.Cleanup(func() { app = saved })

	out, errOut := captureStd(t, func() { require.NoError(t, runConfigGetForAgent("sync.hub_contact", "")) })
	require.Equal(t, "interval:5s\n", out, "stdout reports the effective grandfathered mode, not the activity default")
	_, parseErr := service.ParseHubContact(out[:len(out)-1])
	require.NoError(t, parseErr, "stdout stays a parseable value")
	require.Contains(t, errOut, "grandfathered")
	require.Contains(t, errOut, "mtix config set sync.hub_contact activity")

	app.jsonOutput = true
	out, errOut = captureStd(t, func() { require.NoError(t, runConfigGetForAgent("sync.hub_contact", "")) })
	require.Contains(t, out, `"grandfathered":true`)
	require.Empty(t, errOut)

	_, errOut = captureStd(t, func() { require.NoError(t, runConfigGetForAgent("sync.listen", "")) })
	require.Empty(t, errOut, "other keys carry no notice")
}
