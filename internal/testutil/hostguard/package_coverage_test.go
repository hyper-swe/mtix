// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hostguard_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var touchesUserConfig = regexp.MustCompile(`FromProcess|UserHomeDir|UserConfigDir|XDG_CONFIG_HOME|"HOME"|"APPDATA"|OperatorState\(`)

// TestTestPackages_TouchingUserConfig_InstallTheGuard walks every test package
// under cmd and internal; any package whose tests resolve the user config or
// home directory must install hostguard.Run in a TestMain (MTIX-112.39).
func TestTestPackages_TouchingUserConfig_InstallTheGuard(t *testing.T) {
	repo := filepath.Join("..", "..", "..")
	touching, guarded := map[string]string{}, map[string]bool{}
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repo, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
				return err
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			dir := filepath.Dir(path)
			if touchesUserConfig.Match(body) {
				touching[dir] = path
			}
			if strings.Contains(string(body), "hostguard.Run(") {
				guarded[dir] = true
			}
			return nil
		})
		require.NoError(t, err)
	}
	require.NotEmpty(t, touching)
	for dir, example := range touching {
		require.True(t, guarded[dir], "%s resolves user config (for example in %s) but has no TestMain calling hostguard.Run", dir, filepath.Base(example))
	}
}
