// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Identity of a 0.5.x daemon behind a PID file (ADR-007 D11, MTIX-112.17.15).
package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/procinfo"
)

func TestPidIsLegacyDaemon_Table(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(root, link))
	mtix := func(args ...string) procinfo.Proc {
		return procinfo.Proc{Name: "mtix", Argv0: "mtix", Exe: "/usr/local/bin/mtix", Args: append([]string{"mtix"}, args...)}
	}
	parent, child := filepath.Dir(root), filepath.Base(root)
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
		{"unrelated-program", procinfo.Proc{Name: "sleep", Argv0: "sleep", Exe: "/bin/sleep", Args: []string{"sleep", "30"}}, nil, false},
		{"daemon-with-C", mtix("-C", root, "daemon", "--interval", "5"), nil, true},
		{"daemon-with-long-flag", mtix("--project-dir", root, "daemon"), nil, true},
		{"daemon-with-long-flag-equals", mtix("--project-dir="+root, "daemon"), nil, true},
		{"daemon-with-attached-C", withCwd(mtix("-C"+root, "daemon"), other), nil, true},
		{"daemon-with-attached-C-equals", withCwd(mtix("-C="+root, "daemon"), other), nil, true},
		{"attached-C-names-another-project", withCwd(mtix("-C"+other, "daemon"), root), nil, false},
		{"sync-daemon-spelling", mtix("-C", root, "sync", "daemon"), nil, true},
		{"daemon-root-through-symlink", mtix("-C", link, "daemon"), nil, true},
		{"another-projects-daemon", mtix("-C", other, "daemon"), nil, false},
		{"mtix-mcp-server", mtix("-C", root, "mcp"), nil, false},
		{"mtix-sync-pull", mtix("-C", root, "sync", "pull"), nil, false},
		{"mtix-dispatcher-run", mtix("dispatcher", "run"), nil, false},
		{"mtix-arguments-unreadable-fails-safe", procinfo.Proc{Name: "mtix"}, nil, true},
		{"daemon-no-C-cwd-matches", withCwd(mtix("daemon"), root), nil, true},
		{"daemon-no-C-cwd-differs", withCwd(mtix("daemon"), other), nil, false},
		{"daemon-no-C-cwd-unknown-fails-safe", mtix("daemon"), nil, true},
		{"log-level-before-daemon-cwd-matches", withCwd(mtix("--log-level", "debug", "daemon"), root), nil, true},
		{"log-level-before-daemon-cwd-differs", withCwd(mtix("--log-level", "debug", "daemon"), other), nil, false},
		{"log-level-equals-before-daemon", withCwd(mtix("--log-level=debug", "daemon"), root), nil, true},
		{"log-level-then-C-matches", mtix("--log-level", "debug", "-C", root, "daemon"), nil, true},
		{"log-level-then-C-other", mtix("--log-level", "debug", "-C", other, "daemon"), nil, false},
		{"json-flag-before-daemon", mtix("--json", "-C", root, "daemon"), nil, true},
		{"daemon-interval-value-is-not-a-subcommand", mtix("-C", root, "daemon", "--interval", "5"), nil, true},
		{"relative-C-dot-cwd-matches", withCwd(mtix("-C", ".", "daemon"), root), nil, true},
		{"relative-C-dot-cwd-differs", withCwd(mtix("-C", ".", "daemon"), other), nil, false},
		{"relative-C-name-resolves-against-daemon-cwd", withCwd(mtix("-C", child, "daemon"), parent), nil, true},
		{"relative-project-dir-resolves-against-daemon-cwd", withCwd(mtix("--project-dir", child, "daemon"), parent), nil, true},
		{"relative-C-resolves-through-symlinked-cwd", withCwd(mtix("-C", ".", "daemon"), link), nil, true},
		{"relative-C-with-unknown-cwd-fails-safe", mtix("-C", ".", "daemon"), nil, true},
		{"daemon-status", mtix("-C", root, "daemon", "status"), nil, false},
		{"daemon-install", mtix("-C", root, "daemon", "install"), nil, false},
		{"daemon-uninstall", mtix("-C", root, "daemon", "uninstall"), nil, false},
		{"daemon-start", mtix("-C", root, "daemon", "start"), nil, false},
		{"daemon-stop", mtix("-C", root, "daemon", "stop"), nil, false},
		{"sync-daemon-status", mtix("-C", root, "sync", "daemon", "status"), nil, false},
		{"windows-style-exe", procinfo.Proc{Exe: `C:\Program Files\mtix\mtix.exe`, Args: []string{"mtix", "-C", root, "daemon"}}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(int) (procinfo.Proc, error) { return tt.proc, tt.err }
			require.Equal(t, tt.isLive, pidIsLegacyDaemon(lookup, 4242, root))
		})
	}
}

func withCwd(p procinfo.Proc, cwd string) procinfo.Proc {
	p.Cwd = cwd
	return p
}

func TestParseDaemonArgs_Table(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantDaemon bool
		wantRoot   string
	}{
		{"plain", []string{"mtix", "daemon"}, true, ""},
		{"root-and-flags", []string{"mtix", "-C", "/p", "daemon", "--interval", "3"}, true, "/p"},
		{"sync-daemon", []string{"mtix", "sync", "daemon"}, true, ""},
		{"daemon-as-flag-value-is-not-a-subcommand", []string{"mtix", "-C", "daemon", "status"}, false, "daemon"},
		{"other-subcommand", []string{"mtix", "show", "daemon"}, false, ""},
		{"only-argv0", []string{"mtix"}, false, ""},
		{"log-level-before-daemon", []string{"mtix", "--log-level", "debug", "daemon"}, true, ""},
		{"log-level-equals-before-daemon", []string{"mtix", "--log-level=debug", "daemon"}, true, ""},
		{"log-level-value-named-daemon", []string{"mtix", "--log-level", "daemon", "show"}, false, ""},
		{"interval-value-is-not-a-subcommand", []string{"mtix", "daemon", "--interval", "5"}, true, ""},
		{"service-subcommands", []string{"mtix", "daemon", "status"}, false, ""},
		{"sync-daemon-service-subcommand", []string{"mtix", "sync", "daemon", "install"}, false, ""},
		{"dangling-C", []string{"mtix", "-C"}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon, root := parseDaemonArgs(tt.args)
			require.Equal(t, tt.wantDaemon, daemon)
			require.Equal(t, tt.wantRoot, root)
		})
	}
}
