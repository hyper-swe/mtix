// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func inputState(t *testing.T) *State {
	t.Helper()
	root, err := filepath.EvalSymlinks(safeFixtureRoot(t))
	require.NoError(t, err)
	env := Env{GOOS: runtime.GOOS, Home: "/home/test", root: root}
	if runtime.GOOS == "windows" {
		env.Home = `C:\Users\test`
		env.Values = map[string]string{"APPDATA": `C:\Users\test\AppData\Roaming`}
	}
	return New(env)
}

func TestReadJSON_ReplacedOrAbsentContent_ClearsDestination(t *testing.T) {
	for _, kind := range []string{"directory", "record", "foreign", "empty", "removed", "invalid", "name", "type", "partial", "location", "null"} {
		t.Run(kind, func(t *testing.T) {
			s := inputState(t)
			if kind != "directory" {
				require.NoError(t, s.WriteJSON("hooks", map[string]bool{}))
			}
			name := prepareInput(t, s, kind)
			got := map[string]bool{"old": true}
			fields := struct {
				Keep bool `json:"keep"`
				Old  bool `json:"old"`
			}{Old: true}
			for i, target := range []any{&got, &fields} {
				t.Run([]string{"map", "struct"}[i], func(t *testing.T) {
					reports, err := s.ReadJSON(name, target)
					if kind == "invalid" || kind == "name" || kind == "type" || kind == "partial" || kind == "location" {
						require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
					} else {
						require.NoError(t, err)
					}
					if kind == "foreign" {
						require.Len(t, reports, 1)
					}
					if i == 0 {
						require.False(t, got["old"])
						require.Equal(t, kind == "removed", got["keep"])
					} else {
						require.False(t, fields.Old)
						require.Equal(t, kind == "removed", fields.Keep)
					}
				})
			}

		})
	}
}

func (s *State) pathForTest(t *testing.T) string {
	t.Helper()
	path, err := s.Path()
	require.NoError(t, err)
	return path
}

func TestWriteJSON_SizeBoundary_PreservesAndRepairs(t *testing.T) {
	s := inputState(t)
	overhead, err := json.Marshal(record{HostID: strings.Repeat("0", 32), Data: json.RawMessage(`""`)})
	require.NoError(t, err)
	payload := strings.Repeat("a", (1<<20)-len(overhead))
	require.NoError(t, s.WriteJSON("hooks", payload))
	original, err := os.ReadFile(filepath.Join(s.pathForTest(t), "hooks.json"))
	require.NoError(t, err)
	require.Len(t, original, 1<<20)
	var got string
	_, err = s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, payload, got)
	require.ErrorIs(t, s.WriteJSON("hooks", payload+"b"), model.ErrOperatorStateUnreadable)
	after, err := os.ReadFile(filepath.Join(s.pathForTest(t), "hooks.json"))
	require.NoError(t, err)
	require.Equal(t, original, after)
	require.NoError(t, s.WriteJSON("hooks", "replacement"))
	_, err = s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, "replacement", got)
}

func TestReadJSON_InvalidDestination_Refuses(t *testing.T) {
	s := inputState(t)
	for _, tc := range []struct {
		name   string
		target any
	}{{"nil", nil}, {"map_value", map[string]int{}}, {"nil_pointer", (*map[string]int)(nil)}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ReadJSON("hooks", tc.target)
			require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
		})
	}
}

func prepareInput(t *testing.T, s *State, kind string) string {
	t.Helper()
	switch kind {
	case "record":
		require.NoError(t, os.Remove(filepath.Join(s.pathForTest(t), "hooks.json")))
	case "foreign":
		body := `{"host_id":"00000000000000000000000000000000","data":{}}`
		require.NoError(t, os.WriteFile(filepath.Join(s.pathForTest(t), "hooks.json"), []byte(body), 0600))
	case "location":
		logical, err := Dir(s.env)
		require.NoError(t, err)
		s.env.Values = map[string]string{"CODEX_HOME": logical}
	case "partial":
		require.NoError(t, s.WriteJSON("hooks", map[string]any{"keep": true, "old": "invalid"}))
	case "null":
		require.NoError(t, s.WriteJSON("hooks", nil))
	case "removed":
		require.NoError(t, s.WriteJSON("hooks", map[string]bool{"keep": true}))
	case "invalid":
		require.NoError(t, os.WriteFile(filepath.Join(s.pathForTest(t), "hooks.json"), []byte("{"), 0600))
	case "name":
		return "../hooks"
	case "type":
		require.NoError(t, s.WriteJSON("hooks", "wrong type"))
	}
	return "hooks"
}

func safeFixtureRoot(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	root, err := os.MkdirTemp(home, ".mtix-state-test-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	return root
}
