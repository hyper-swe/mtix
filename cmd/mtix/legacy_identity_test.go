// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Identity of a 0.5.x daemon behind a PID file (ADR-007 D11, MTIX-112.17.15).
package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/procinfo"
)

// projectWithMtix makes a project root (symlinks resolved, as /proc reports a cwd) with a
// .mtix directory and a subdirectory.
func projectWithMtix(t *testing.T) (root, sub string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".mtix"), 0o700))
	sub = filepath.Join(root, "pkg", "deep")
	require.NoError(t, os.MkdirAll(sub, 0o700))
	return root, sub
}

func TestPidIsLegacyDaemon_Table(t *testing.T) {
	root, sub := projectWithMtix(t)
	other, otherSub := projectWithMtix(t)
	bare, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(root, link))
	mtix := func(cwd string, args ...string) procinfo.Proc {
		return procinfo.Proc{Name: "mtix", Argv0: "mtix", Exe: "/usr/local/bin/mtix", Args: append([]string{"mtix"}, args...), Cwd: cwd}
	}
	tests := []struct {
		name   string
		proc   procinfo.Proc
		err    error
		isLive bool
	}{
		{"absent-process", procinfo.Proc{}, procinfo.ErrNoProcess, false},
		{"unsupported-platform-fails-safe", procinfo.Proc{}, procinfo.ErrUnsupported, true},
		{"unreadable-process-table-fails-safe", procinfo.Proc{}, errors.New("read process table: permission denied"), true},
		{"no-identity-at-all-fails-safe", procinfo.Proc{StartTime: "1"}, nil, true},
		{"unrelated-program", procinfo.Proc{Name: "sleep", Argv0: "sleep", Exe: "/bin/sleep", Args: []string{"sleep", "30"}, Cwd: root}, nil, false},
		{"mtix-arguments-unreadable-fails-safe", procinfo.Proc{Name: "mtix"}, nil, true},
		{"plain-daemon-cwd-is-root", mtix(root, "daemon", "--interval", "5"), nil, true},
		{"absolute-C-daemon-cwd-is-root", mtix(root, "-C", root, "daemon"), nil, true},
		{"absolute-C-other-project", mtix(other, "-C", other, "daemon"), nil, false},
		{"relative-C-daemon-already-moved-into-root", mtix(root, "-C", "proj", "daemon"), nil, true},
		{"relative-C-dot", mtix(root, "-C", ".", "daemon"), nil, true},
		{"relative-C-other-project", mtix(other, "-C", "proj", "daemon"), nil, false},
		{"project-dir-equals-form", mtix(root, "--project-dir=proj", "daemon"), nil, true},
		{"attached-C-form", mtix(root, "-Cproj", "daemon"), nil, true},
		{"subdirectory-cwd-serves-root", mtix(sub, "daemon"), nil, true},
		{"subdirectory-C-serves-root", mtix(sub, "-C", "pkg/deep", "daemon"), nil, true},
		{"other-project-subdirectory", mtix(otherSub, "daemon"), nil, false},
		{"cwd-without-any-project", mtix(bare, "daemon"), nil, false},
		{"cwd-through-symlink-resolves", mtix(link, "daemon"), nil, true},
		{"cwd-unknown-fails-safe", mtix("", "-C", other, "daemon"), nil, true},
		{"sync-daemon-spelling", mtix(root, "sync", "daemon"), nil, true},
		{"sync-daemon-other-project", mtix(other, "-C", "x", "sync", "daemon"), nil, false},
		{"log-level-value-before-daemon", mtix(root, "--log-level", "debug", "daemon"), nil, true},
		{"log-level-equals-before-daemon", mtix(root, "--log-level=debug", "daemon"), nil, true},
		{"interval-value-before-daemon", mtix(root, "--interval", "30", "daemon"), nil, true},
		{"root-flags-then-C-then-daemon", mtix(root, "--json", "--log-level", "debug", "-C", "p", "daemon"), nil, true},
		{"log-level-before-daemon-other-project", mtix(other, "--log-level", "debug", "daemon"), nil, false},
		{"daemon-status", mtix(root, "-C", root, "daemon", "status"), nil, false},
		{"daemon-install", mtix(root, "daemon", "install"), nil, false},
		{"daemon-uninstall", mtix(root, "daemon", "uninstall"), nil, false},
		{"daemon-start", mtix(root, "daemon", "start"), nil, false},
		{"daemon-stop", mtix(root, "daemon", "stop"), nil, false},
		{"daemon-help", mtix(root, "daemon", "help"), nil, false},
		{"sync-daemon-status", mtix(root, "sync", "daemon", "status"), nil, false},
		{"flag-value-named-daemon-then-verb", mtix(root, "-C", "daemon", "status"), nil, false},
		{"mtix-mcp-server", mtix(root, "-C", root, "mcp"), nil, false},
		{"mtix-sync-pull", mtix(root, "-C", root, "sync", "pull"), nil, false},
		{"mtix-show-daemon-task", mtix(root, "show", "daemon"), nil, false},
		{"mtix-dispatcher-run", mtix(root, "dispatcher", "run"), nil, false},
		{"windows-style-exe", procinfo.Proc{Exe: `C:\Program Files\mtix\mtix.exe`, Args: []string{"mtix", "-C", root, "daemon"}, Cwd: root}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(int) (procinfo.Proc, error) { return tt.proc, tt.err }
			require.Equal(t, tt.isLive, pidIsLegacyDaemon(lookup, 4242, root))
		})
	}
}

func TestRunsDaemon_Table(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"plain", []string{"mtix", "daemon"}, true},
		{"daemon-flags-after", []string{"mtix", "daemon", "--interval", "3"}, true},
		{"sync-daemon", []string{"mtix", "sync", "daemon"}, true},
		{"root-flag-values-before", []string{"mtix", "-C", "/p", "--log-level", "debug", "--interval", "30", "daemon"}, true},
		{"service-verb", []string{"mtix", "daemon", "status"}, false},
		{"bool-flag-then-service-verb", []string{"mtix", "daemon", "--json", "status"}, false},
		{"value-flag-then-service-verb", []string{"mtix", "daemon", "--interval", "30", "status"}, false},
		{"equals-flag-then-service-verb", []string{"mtix", "daemon", "--interval=30", "stop"}, false},
		{"equals-flag-before-other-subcommand", []string{"mtix", "--log-level=debug", "show", "daemon"}, false},
		{"short-flag-value-named-verb", []string{"mtix", "daemon", "-C", "stop"}, true},
		{"log-level-value-named-verb", []string{"mtix", "daemon", "--log-level", "status"}, true},
		{"project-dir-value-named-verb", []string{"mtix", "daemon", "--project-dir", "install"}, true},
		{"equals-log-level-then-verb", []string{"mtix", "daemon", "--log-level=debug", "stop"}, false},
		{"value-flag-value-then-verb", []string{"mtix", "daemon", "-C", "stop", "status"}, false},
		{"dangling-value-flag-after-daemon", []string{"mtix", "daemon", "--log-level"}, true},
		{"sync-value-named-verb", []string{"mtix", "sync", "daemon", "--interval", "start"}, true},
		{"sync-service-verb", []string{"mtix", "sync", "daemon", "install"}, false},
		{"daemon-as-flag-value-then-verb", []string{"mtix", "-C", "daemon", "status"}, false},
		{"other-subcommand", []string{"mtix", "show", "daemon"}, false},
		{"sync-other-subcommand", []string{"mtix", "sync", "pull"}, false},
		{"only-argv0", []string{"mtix"}, false},
		{"dangling-C", []string{"mtix", "-C"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, runsDaemon(tt.args))
		})
	}
}

// TestDaemonValueFlags_MatchCommandTree walks the real command tree under daemon and sync
// daemon (root persistent flags included) and fails when a non-bool flag is missing from the
// skip set, or when the set names a flag that no longer exists as a non-bool flag.
func TestDaemonValueFlags_MatchCommandTree(t *testing.T) {
	root := newRootCmd()
	found := map[string]bool{}
	for _, cmd := range daemonCommandsUnder(t, root) {
		collect := func(f *pflag.Flag) {
			if f.Value.Type() == "bool" {
				return
			}
			found["--"+f.Name] = true
			if f.Shorthand != "" {
				found["-"+f.Shorthand] = true
			}
		}
		cmd.Flags().VisitAll(collect)
		cmd.InheritedFlags().VisitAll(collect)
	}
	for flag := range found {
		require.Truef(t, daemonValueFlags[flag], "non-bool flag %s is missing from daemonValueFlags", flag)
	}
	for flag := range daemonValueFlags {
		require.Truef(t, found[flag], "daemonValueFlags names %s, which is no longer a non-bool daemon flag", flag)
	}
}

// daemonCommandsUnder returns daemon, sync daemon and every subcommand of both.
func daemonCommandsUnder(t *testing.T, root *cobra.Command) []*cobra.Command {
	t.Helper()
	var out []*cobra.Command
	for _, path := range [][]string{{"daemon"}, {"sync", "daemon"}} {
		cmd, _, err := root.Find(path)
		require.NoError(t, err)
		require.Equal(t, "daemon", cmd.Name())
		out = append(out, cmd)
		out = append(out, cmd.Commands()...)
	}
	return out
}
