// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package procinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyMtixDaemon_ByArgsAndCwd_ReturnsRole(t *testing.T) {
	const proj = "/work/proj"
	tests := []struct {
		name string
		p    Proc
		want DaemonRole
	}{
		{"listener", Proc{Args: []string{"/usr/bin/mtix", "daemon", "--listen"}, Cwd: proj}, RoleListener},
		{"listener cwd unknown", Proc{Args: []string{"mtix", "daemon", "--listen"}}, RoleListener},
		{"listener windows exe", Proc{Args: []string{`C:\bin\mtix.exe`, "daemon", "--listen"}}, RoleListener},
		{"service daemon", Proc{Args: []string{"/usr/bin/mtix", "daemon", "--interval", "5"}, Cwd: proj}, RoleDaemon},
		{"sync daemon", Proc{Args: []string{"mtix", "sync", "daemon"}}, RoleDaemon},
		{"listener other cwd", Proc{Args: []string{"mtix", "daemon", "--listen"}, Cwd: "/work/other"}, RoleUnknown},
		{"listener extra flags", Proc{Args: []string{"mtix", "daemon", "--listen", "--interval", "1"}}, RoleUnknown},
		{"args unreadable", Proc{}, RoleUnknown},
		{"not mtix", Proc{Args: []string{"/bin/sleep", "daemon", "--listen"}}, RoleUnknown},
		{"other verb", Proc{Args: []string{"mtix", "inbox", "--wait"}}, RoleUnknown},
		{"bare name mtix", Proc{Args: []string{"mtix", "daemon"}}, RoleDaemon},
		{"bare name mtix.exe", Proc{Args: []string{"mtix.exe", "daemon"}}, RoleDaemon},
		{"unix path mtix", Proc{Args: []string{"/usr/local/bin/mtix", "daemon"}}, RoleDaemon},
		{"windows path mtix.exe", Proc{Args: []string{`C:\bin\mtix.exe`, "daemon"}}, RoleDaemon},
		{"name mtix-x", Proc{Args: []string{"mtix-x", "daemon"}}, RoleUnknown},
		{"name mtixd", Proc{Args: []string{"mtixd", "daemon"}}, RoleUnknown},
		{"name xmtix", Proc{Args: []string{"xmtix", "daemon"}}, RoleUnknown},
		{"name mtix-x listener", Proc{Args: []string{"/opt/mtix-x", "daemon", "--listen"}}, RoleUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, why := ClassifyMtixDaemon(tt.p, proj)
			assert.Equal(t, tt.want, got)
			if tt.want == RoleUnknown {
				assert.NotEmpty(t, why, "an unknown role always says why")
			}
		})
	}
}
