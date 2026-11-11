// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// scratch returns a resolved scratch root with a proj dir and a settings
// file inside, plus an unrelated directory outside it.
func scratch(t *testing.T) (root, outside string) {
	t.Helper()
	// A short directory under the OS temp dir keeps unix socket paths within
	// the platform limit (about 104 bytes on macOS).
	dir, err := os.MkdirTemp("", "lsg")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	base, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	root = filepath.Join(base, "run")
	outside = filepath.Join(base, "elsewhere")
	for _, d := range []string{filepath.Join(root, "proj"), filepath.Join(root, "cfg"), outside} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "cfg", "s.json"), []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "s.json"), []byte("{}"), 0o600))
	return root, outside
}

func goodLaunch(root string) Launch {
	return Launch{
		Root: root,
		Cwd:  filepath.Join(root, "proj"),
		Args: []string{"--model", "haiku", "--settings", filepath.Join(root, "cfg", "s.json"),
			"--setting-sources", "project,local"},
	}
}

func TestCheckLaunch_ScratchOnlyPlan_Passes(t *testing.T) {
	root, _ := scratch(t)
	require.NoError(t, CheckLaunch(goodLaunch(root)))
}

func TestCheckLaunch_SettingsEqualsForm_Passes(t *testing.T) {
	root, _ := scratch(t)
	l := goodLaunch(root)
	l.Args = []string{"--settings=" + filepath.Join(root, "cfg", "s.json"), "--setting-sources=project"}
	require.NoError(t, CheckLaunch(l))
}

func TestCheckLaunch_Refusals_ReturnErrLaunchRefused(t *testing.T) {
	root, outside := scratch(t)
	link := filepath.Join(root, "proj", "escape")
	require.NoError(t, os.Symlink(outside, link))
	settings := filepath.Join(root, "cfg", "s.json")
	tests := []struct {
		name   string
		mutate func(l *Launch)
		want   string
	}{
		{"cwd outside root", func(l *Launch) { l.Cwd = outside }, "cwd"},
		{"cwd escapes through a symlink", func(l *Launch) { l.Cwd = link }, "cwd"},
		{"cwd uses dot-dot", func(l *Launch) { l.Cwd = filepath.Join(root, "proj", "..", "..", "elsewhere") }, "cwd"},
		{"cwd is a sibling sharing the root prefix", func(l *Launch) { l.Cwd = root + "-other" }, "cwd"},
		{"relative root", func(l *Launch) { l.Root = "run" }, "root"},
		{"filesystem root as scratch root", func(l *Launch) { l.Root = string(filepath.Separator) }, "root"},
		{"settings outside root", func(l *Launch) {
			l.Args = []string{"--settings", filepath.Join(outside, "s.json")}
		}, "settings"},
		{"one of two settings outside root", func(l *Launch) {
			l.Args = append(l.Args, "--settings", filepath.Join(outside, "s.json"))
		}, "settings"},
		{"inline settings json", func(l *Launch) { l.Args = []string{"--settings", `{"hooks":{}}`} }, "settings"},
		{"no settings at all", func(l *Launch) { l.Args = []string{"--model", "haiku"} }, "settings"},
		{"settings flag without value", func(l *Launch) { l.Args = []string{"--settings"} }, "settings"},
		{"skip permissions flag", func(l *Launch) {
			l.Args = []string{"--dangerously-skip-permissions", "--settings", settings}
		}, "permission"},
		{"allow skip permissions flag", func(l *Launch) {
			l.Args = []string{"--allow-dangerously-skip-permissions", "--settings", settings}
		}, "permission"},
		{"bypass mode spaced", func(l *Launch) {
			l.Args = []string{"--permission-mode", "bypassPermissions", "--settings", settings}
		}, "permission"},
		{"bypass mode equals", func(l *Launch) {
			l.Args = []string{"--permission-mode=bypassPermissions", "--settings", settings}
		}, "permission"},
		{"settings file sets bypass mode", func(l *Launch) {
			l.Args[3] = writeSettings(t, root, "bypass.json", `{"permissions":{"defaultMode":"bypassPermissions"}}`)
		}, "defaultMode"},
		{"settings file bypass mode in other case", func(l *Launch) {
			l.Args[3] = writeSettings(t, root, "bypass-case.json", `{"permissions":{"defaultMode":"BypassPermissions"}}`)
		}, "defaultMode"},
		{"settings file is not json", func(l *Launch) {
			l.Args[3] = writeSettings(t, root, "bad.json", `{not json`)
		}, "settings"},
		{"project settings file sets bypass mode", func(l *Launch) {
			writeSettingsAt(t, filepath.Join(l.Cwd, ".claude"), "settings.json", `{"permissions":{"defaultMode":"bypassPermissions"}}`)
		}, "defaultMode"},
		{"project local settings file sets bypass mode", func(l *Launch) {
			writeSettingsAt(t, filepath.Join(l.Cwd, ".claude"), "settings.local.json", `{"permissions":{"defaultMode":"bypassPermissions"}}`)
		}, "defaultMode"},
		{"user settings are loaded: no config dir and no setting sources", func(l *Launch) {
			l.Args = l.Args[:4]
		}, "user settings"},
		{"setting sources include user", func(l *Launch) { l.Args[5] = "user,project" }, "user settings"},
		{"setting sources include user in other case", func(l *Launch) { l.Args[5] = "project, USER" }, "user settings"},
		{"setting sources equals form includes user", func(l *Launch) {
			l.Args = append(l.Args[:4], "--setting-sources=user")
		}, "user settings"},
		{"add-dir outside root", func(l *Launch) { l.Args = append(l.Args, "--add-dir", outside) }, "add-dir"},
		{"add-dir equals form outside root", func(l *Launch) { l.Args = append(l.Args, "--add-dir="+outside) }, "add-dir"},
		{"second add-dir value outside root", func(l *Launch) {
			l.Args = append(l.Args, "--add-dir", filepath.Join(root, "proj"), outside)
		}, "add-dir"},
		{"messaging socket env outside root", func(l *Launch) {
			l.Env = []string{"CLAUDE_CODE_MESSAGING_SOCKET=" + filepath.Join(outside, "m.sock")}
		}, "socket"},
		{"config dir env outside root", func(l *Launch) {
			l.Env = []string{"CLAUDE_CONFIG_DIR=" + outside}
		}, "config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Rows may plant project settings; keep rows independent.
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(filepath.Join(root, "proj", ".claude"))) })
			l := goodLaunch(root)
			tt.mutate(&l)
			err := CheckLaunch(l)
			require.ErrorIs(t, err, ErrLaunchRefused)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestCheckLaunch_ConfigDirInsideRootAndManualMode_Passes(t *testing.T) {
	root, _ := scratch(t)
	l := goodLaunch(root)
	l.Env = []string{"PATH=/usr/bin", "CLAUDE_CONFIG_DIR=" + filepath.Join(root, "cfg")}
	l.Args = append(l.Args, "--permission-mode", "default")
	require.NoError(t, CheckLaunch(l))
}

func TestCheckLaunch_NotYetExistingSettingsInsideRoot_Passes(t *testing.T) {
	root, _ := scratch(t)
	l := goodLaunch(root)
	l.Args = []string{"--settings", filepath.Join(root, "cfg", "later", "s-new.json"), "--setting-sources", "local"}
	require.NoError(t, CheckLaunch(l))
}

// socketAt listens on a unix socket path and returns it.
func socketAt(t *testing.T, path string) string {
	t.Helper()
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, l.Close()) })
	return path
}

func ownRecord(root, sock string, pid int) Record {
	return Record{
		Present:    map[string]bool{"pid": true, "cwd": true, "messagingSocketPath": true},
		PID:        pid,
		Cwd:        filepath.Join(root, "proj"),
		SocketPath: sock,
	}
}

func TestCheckSocket_OwnChildSocket_Passes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not the listener transport on windows")
	}
	root, _ := scratch(t)
	sock := socketAt(t, filepath.Join(root, "s.sock"))
	require.NoError(t, CheckSocket(root, sock, 4242, ownRecord(root, sock, 4242)))
}

func TestCheckSocket_Refusals_ReturnErrLaunchRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not the listener transport on windows")
	}
	root, outside := scratch(t)
	sock := socketAt(t, filepath.Join(root, "s.sock"))
	foreign := socketAt(t, filepath.Join(outside, "f.sock"))
	plain := filepath.Join(root, "plain")
	require.NoError(t, os.WriteFile(plain, nil, 0o600))
	tests := []struct {
		name string
		path string
		pid  int
		rec  Record
		want string
	}{
		{"registry pid is not the child", sock, 4242, ownRecord(root, sock, 7), "pid"},
		{"registry names another socket", sock, 4242, ownRecord(root, foreign, 4242), "registry"},
		{"foreign socket named by own registry but cwd outside root", foreign, 4242,
			Record{Present: map[string]bool{"pid": true, "cwd": true, "messagingSocketPath": true},
				PID: 4242, Cwd: outside, SocketPath: foreign}, "cwd"},
		{"path is not a socket", plain, 4242, ownRecord(root, plain, 4242), "socket"},
		{"missing socket", filepath.Join(root, "gone.sock"), 4242, ownRecord(root, filepath.Join(root, "gone.sock"), 4242), "socket"},
		{"record lacks the socket key", sock, 4242, Record{Present: map[string]bool{"pid": true}, PID: 4242}, "registry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckSocket(root, tt.path, tt.pid, tt.rec)
			require.ErrorIs(t, err, ErrLaunchRefused)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestCheckSocket_ForeignSocketOutsideRoot_Refused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not the listener transport on windows")
	}
	root, outside := scratch(t)
	foreign := socketAt(t, filepath.Join(outside, "f.sock"))
	// Registry says pid 4242 and a root-side cwd, but the socket is not the
	// registry's own socket for that child: still refused.
	rec := ownRecord(root, filepath.Join(root, "s.sock"), 4242)
	err := CheckSocket(root, foreign, 4242, rec)
	require.ErrorIs(t, err, ErrLaunchRefused)
}

func writeSettings(t *testing.T, root, name, body string) string {
	t.Helper()
	return writeSettingsAt(t, filepath.Join(root, "cfg"), name, body)
}

func writeSettingsAt(t *testing.T, dir, name, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func TestCheckLaunch_IsolationAlternatives_Pass(t *testing.T) {
	root, _ := scratch(t)
	tests := []struct {
		name   string
		mutate func(l *Launch)
	}{
		{"config dir inside root and no setting sources", func(l *Launch) {
			l.Args = l.Args[:4]
			l.Env = []string{"CLAUDE_CONFIG_DIR=" + filepath.Join(root, "cfg")}
		}},
		{"add-dir inside root", func(l *Launch) {
			l.Args = append(l.Args, "--add-dir", filepath.Join(root, "proj"))
		}},
		{"messaging socket env inside root", func(l *Launch) {
			l.Env = []string{"CLAUDE_CODE_MESSAGING_SOCKET=" + filepath.Join(root, "m.sock")}
		}},
		{"settings with a harmless default mode", func(l *Launch) {
			l.Args[3] = writeSettings(t, root, "ok.json", `{"permissions":{"defaultMode":"default"},"hooks":{}}`)
		}},
		{"project settings without permissions", func(l *Launch) {
			writeSettingsAt(t, filepath.Join(l.Cwd, ".claude"), "settings.json", `{"model":"haiku"}`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(filepath.Join(root, "proj", ".claude"))) })
			l := goodLaunch(root)
			tt.mutate(&l)
			require.NoError(t, CheckLaunch(l))
		})
	}
}

func TestCheckSocket_OwnChildSocketOutsideRoot_Refused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not the listener transport on windows")
	}
	root, outside := scratch(t)
	sock := socketAt(t, filepath.Join(outside, "o.sock"))
	// The registry record proves the socket is the child's own, but the
	// story still requires it to live inside the scratch root.
	err := CheckSocket(root, sock, 4242, ownRecord(root, sock, 4242))
	require.ErrorIs(t, err, ErrLaunchRefused)
	require.Contains(t, err.Error(), "outside the scratch root")
}
