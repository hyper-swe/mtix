// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hostguard_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

func TestMain(m *testing.M) { os.Exit(hostguard.Run(m)) }

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func TestRun_UnderTestMain_ProcessEnvironmentIsIsolated(t *testing.T) {
	require.NoError(t, hostguard.Assert())
	root := os.Getenv(hostguard.RootEnv)
	require.NotEmpty(t, root)
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA"} {
		assert.True(t, within(root, os.Getenv(key)), "%s=%s must live under the scratch root", key, os.Getenv(key))
	}
}

func TestRun_UnderTestMain_TmpdirIsASiblingOfTheHome(t *testing.T) {
	home, tmp := os.Getenv("HOME"), os.Getenv("TMPDIR")
	if tmp == "" {
		t.Skip("TMPDIR is not used on this platform")
	}
	assert.False(t, within(tmp, home), "home must not sit inside the repointed TMPDIR")
	assert.False(t, within(home, tmp), "TMPDIR must not sit inside the home")
}

func TestIsolate_PerTest_UsesFreshRootAndCleansUp(t *testing.T) {
	outer := os.Getenv(hostguard.RootEnv)
	var layout hostguard.Layout
	t.Run("inner", func(t *testing.T) {
		layout = hostguard.Isolate(t)
		require.NoError(t, hostguard.Assert())
		assert.NotEqual(t, outer, layout.Root)
		assert.Equal(t, layout.Home, os.Getenv("HOME"))
		assert.Equal(t, layout.Config, os.Getenv("XDG_CONFIG_HOME"))
		assert.Equal(t, layout.Config, os.Getenv("APPDATA"))
		require.NoError(t, os.WriteFile(filepath.Join(layout.Home, "marker"), []byte("x"), 0o600))
	})
	_, err := os.Stat(layout.Root)
	assert.True(t, os.IsNotExist(err), "scratch root must be removed after the test")
	assert.Equal(t, outer, os.Getenv(hostguard.RootEnv), "env restored")
}

func TestAssert_HarnessMissing_Fails(t *testing.T) {
	t.Setenv(hostguard.RootEnv, "")
	assert.Error(t, hostguard.Assert())
}

func TestAssert_DeliberateUnisolatedWrite_IsCaught(t *testing.T) {
	realHome := filepath.Join(filepath.Dir(os.Getenv(hostguard.RootEnv)), "pretend-real-home")
	cases := []struct{ name, key string }{
		{"home", "HOME"}, {"profile", "USERPROFILE"}, {"xdg", "XDG_CONFIG_HOME"}, {"appdata", "APPDATA"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, filepath.Join(realHome, ".config"))
			err := hostguard.Assert()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.key)
			assert.Error(t, hostguard.AssertPath(filepath.Join(realHome, ".config", "mtix", "wake.json")))
		})
	}
}

func TestAssert_VariableUnset_Fails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	assert.Error(t, hostguard.Assert())
}

func TestAssert_RecordedRealHome_IsRejectedEvenInsideRoot(t *testing.T) {
	t.Setenv(hostguard.RealHomeEnv, os.Getenv("HOME"))
	assert.Error(t, hostguard.Assert())
}

func TestAssertPath_InsideRoot_Passes(t *testing.T) {
	assert.NoError(t, hostguard.AssertPath(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "mtix", "wake.json")))
	assert.Error(t, hostguard.AssertPath(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "..", "..", "..", "x")))
}

func TestChildEnv_ForcesIsolatedVariablesOverInherited(t *testing.T) {
	env := hostguard.ChildEnv(t, []string{"HOME=/real/home", "XDG_CONFIG_HOME=/real/xdg", "KEEP=1"})
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	assert.Equal(t, os.Getenv("HOME"), got["HOME"])
	assert.Equal(t, os.Getenv("XDG_CONFIG_HOME"), got["XDG_CONFIG_HOME"])
	assert.Equal(t, os.Getenv("APPDATA"), got["APPDATA"])
	assert.Equal(t, os.Getenv("USERPROFILE"), got["USERPROFILE"])
	assert.Equal(t, os.Getenv(hostguard.RootEnv), got[hostguard.RootEnv])
	assert.Equal(t, "1", got["KEEP"])
	assert.NotContains(t, strings.Join(env, "\n"), "/real/")
}

func TestRemoveTree_NotAScratchRoot_Refuses(t *testing.T) {
	victim := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(victim, "keep"), []byte("x"), 0o600))
	require.Error(t, hostguard.RemoveTreeForTest(victim))
	require.Error(t, hostguard.RemoveTreeForTest("relative"))
	_, err := os.Stat(filepath.Join(victim, "keep"))
	require.NoError(t, err)
}

func TestSharedTemp_SystemTempRoots_AreRejectedAsBases(t *testing.T) {
	for _, dir := range []string{"/tmp", "/tmp/x", "/var/tmp", "/private/tmp/y"} {
		assert.True(t, hostguard.SharedTempForTest(dir), dir)
	}
	assert.False(t, hostguard.SharedTempForTest(t.TempDir()))
}

func TestChooseBase_TableOfTempConfigurations(t *testing.T) {
	const home = "/home/dev"
	tests := []struct {
		name    string
		env     map[string]string
		temp    string
		want    string
		wantErr bool
	}{
		{"TMPDIR=/tmp", nil, "/tmp", "", true},
		{"unset (Go reports /tmp)", nil, "/tmp", "", true},
		{"var tmp", nil, "/var/tmp", "", true},
		{"custom private dir", nil, "/data/scratch", "/data/scratch", false},
		{"macOS per-user temp", nil, "/var/folders/ab/cd/T", "/var/folders/ab/cd/T", false},
		{"home-rooted TMPDIR", nil, home + "/tmp", "", true},
		{"home itself", nil, home, "", true},
		{"original temp wins", map[string]string{hostguard.OriginalTmpEnv: "/data/orig"}, "/data/other", "/data/orig", false},
		{"explicit scratch base", map[string]string{hostguard.ScratchBaseEnv: "/ci/scratch"}, "/tmp", "/ci/scratch", false},
		{"explicit base inside home refused", map[string]string{hostguard.ScratchBaseEnv: home + "/x"}, "/tmp", "", true},
		{"runtime dir", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, "/tmp", "/run/user/1000", false},
		{"CI runner temp under home accepted", map[string]string{"GITHUB_ACTIONS": "true", "RUNNER_TEMP": home + "/work/_temp"}, "/tmp", home + "/work/_temp", false},
		{"RUNNER_TEMP ignored outside CI", map[string]string{"RUNNER_TEMP": home + "/work/_temp"}, "/tmp", "", true},
		{"CI runner temp in /tmp refused", map[string]string{"GITHUB_ACTIONS": "true", "RUNNER_TEMP": "/tmp/r"}, "/tmp", "", true},
		{"relative base refused", map[string]string{hostguard.ScratchBaseEnv: "rel"}, "/tmp", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hostguard.ChooseBaseForTest(tc.env, tc.temp, home)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), hostguard.ScratchBaseEnv)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRun_GoToolEnvironment_StaysOutsideScratchHome(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not on PATH")
	}
	root := os.Getenv(hostguard.RootEnv)
	for _, name := range []string{"GOMODCACHE", "GOCACHE", "GOPATH"} {
		out, runErr := exec.Command(goTool, "env", name).Output()
		require.NoError(t, runErr)
		value := strings.TrimSpace(string(out))
		require.NotEmpty(t, value, name)
		assert.False(t, within(root, value), "%s=%s must not relocate under the scratch root", name, value)
		assert.Equal(t, os.Getenv(name), value, "%s must be exported unchanged before HOME moved", name)
	}
}

func TestRealCacheDir_RecordedBeforeIsolation_IsOutsideScratchRoot(t *testing.T) {
	dir, err := hostguard.RealCacheDir()
	require.NoError(t, err)
	assert.False(t, within(os.Getenv(hostguard.RootEnv), dir))
}

func TestEnviron_HomeOutsideRoot_Panics(t *testing.T) {
	assert.NotPanics(t, func() { _ = hostguard.Environ() })
	t.Setenv("HOME", filepath.Join(filepath.Dir(os.Getenv(hostguard.RootEnv)), "elsewhere"))
	assert.Panics(t, func() { _ = hostguard.Environ() })
}

func TestEnviron_HarnessMissing_Panics(t *testing.T) {
	t.Setenv(hostguard.RootEnv, "")
	assert.Panics(t, func() { _ = hostguard.Environ() })
}
