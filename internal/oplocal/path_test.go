// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDir_ConfiguredPaths_UsesPlatformDirectory(t *testing.T) {
	for _, tc := range []struct{ platform, home, key, value, want string }{
		{"darwin", "/home/test", "", "", "/home/test/.config/mtix"},
		{"linux", "/home/test", "XDG_CONFIG_HOME", "/config", "/config/mtix"},
		{"windows", `C:\Users\test`, "APPDATA", `C:\Users\test\AppData\Roaming`, `C:\Users\test\AppData\Roaming\mtix`},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			dir, err := Dir(Env{GOOS: tc.platform, Home: tc.home, Values: map[string]string{tc.key: tc.value}})
			require.NoError(t, err)
			require.Equal(t, tc.want, dir)
		})
	}
}
func TestDir_RelativeOrMissingBase_Refuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  Env
	}{
		{"missing_home", Env{GOOS: "linux"}}, {"relative_home", Env{GOOS: "linux", Home: "relative"}},
		{"relative_xdg", Env{GOOS: "linux", Values: map[string]string{"XDG_CONFIG_HOME": "relative"}}},
		{"relative_appdata", Env{GOOS: "windows", Values: map[string]string{"APPDATA": "relative"}}},
	} {
		t.Run(tc.name, func(t *testing.T) { _, err := Dir(tc.env); require.ErrorIs(t, err, model.ErrOperatorStateUnreadable) })
	}
}
func TestPath_ExcludedPrefix_Refuses(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows"} {
		t.Run(platform, func(t *testing.T) {
			env := Env{GOOS: platform, Home: "/home/test", Values: map[string]string{"TMPDIR": "/temp", "CODEX_HOME": "/codex"}}
			roots := []string{"/tmp", "/temp", "/codex", "/home/test/.codex"}
			for _, root := range roots {
				require.ErrorIs(t, validateLocation(env, root+"/state"), model.ErrOperatorStateUnreadable)
			}
			if platform == "windows" {
				env.Home = `C:\Users\test`
				env.Values["CODEX_HOME"] = `C:\Codex`
				require.ErrorIs(t, validateLocation(env, `c:\CODEX\state`), model.ErrOperatorStateUnreadable)
			}
			require.NoError(t, validateLocation(env, "/workspace-other/state"))
		})
	}
}
