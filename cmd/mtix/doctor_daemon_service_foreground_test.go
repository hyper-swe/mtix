// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// These tests pin the foreground-daemon advice of the daemon.service-stopped-live
// doctor row and every shipped doc copy that repeats it (MTIX-275.4): a foreground
// process writes the same pid file as the service daemon and `mtix daemon stop`
// cannot end it, so it is ended where it runs before uninstall is suggested.
import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

const (
	foregroundAdviceMtix = "foreground `mtix daemon`"
	foregroundAdviceSync = "`mtix sync daemon`"
	foregroundAdviceEnd  = "end it where it runs (Ctrl-C in its terminal, or close that terminal)"
)

// TestDaemonServiceStateDoctor_StoppedButLive_DetailEndsForegroundDaemonFirst pins that the
// row Detail names a foreground mtix daemon and mtix sync daemon, ending it where it runs,
// BEFORE uninstall is mentioned, and never advises ending a process by pid.
func TestDaemonServiceStateDoctor_StoppedButLive_DetailEndsForegroundDaemonFirst(t *testing.T) {
	fm := &fakeManager{status: func() (string, error) { return "", exitErr{code: 113} }}
	row := runDaemonServiceDoctor(t, installedSpec(t), func() (bool, int, error) { return true, 4242, nil }, fm)
	require.NotNil(t, row)
	assert.Equal(t, service.DoctorWarn, row.Status)
	detail := row.Detail
	for _, want := range []string{"foreground mtix daemon", "mtix sync daemon", "end it where it runs", "Ctrl-C"} {
		assert.Contains(t, detail, want)
	}
	end, uninstall := strings.Index(detail, "Ctrl-C"), strings.Index(detail, "mtix daemon uninstall")
	require.GreaterOrEqual(t, end, 0)
	require.GreaterOrEqual(t, uninstall, 0)
	assert.Less(t, end, uninstall, "ending the foreground process comes before the uninstall advice")
	assert.Contains(t, detail, "or close that terminal")
	onlyIf := strings.Index(detail, "Only if the process keeps running")
	require.GreaterOrEqual(t, onlyIf, 0)
	assert.Less(t, onlyIf, uninstall, "uninstall is conditional on the process still running")
	assert.Contains(t, detail, "never end a process by pid")
	assert.NotContains(t, strings.ToLower(row.Fix+detail), "kill")
}

// TestDaemonServiceStoppedLive_Docs_EndForegroundDaemonBeforeUninstall pins the same advice
// at each shipped doc line that names the row.
func TestDaemonServiceStoppedLive_Docs_EndForegroundDaemonBeforeUninstall(t *testing.T) {
	copies := []string{
		"docs/TROUBLESHOOTING.md",
		"internal/docs/templates/troubleshooting.md.tmpl",
		"internal/docs/templates/skills/sync.md.tmpl",
		".claude-plugin/skills/mtix-sync.md",
		".codex-plugin/skills/sync/SKILL.md",
		"USERMANUAL.md",
	}
	for _, path := range copies {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", path))
			require.NoError(t, err)
			found := 0
			for _, line := range strings.Split(string(raw), "\n") {
				if !strings.Contains(line, daemonServiceStoppedLiveID) {
					continue
				}
				found++
				for _, want := range []string{foregroundAdviceMtix, foregroundAdviceSync, foregroundAdviceEnd} {
					assert.Contains(t, line, want)
				}
				row := line[strings.Index(line, daemonServiceStoppedLiveID):]
				assert.Less(t, strings.Index(row, foregroundAdviceEnd), strings.Index(row, "`mtix daemon uninstall`"),
					"ending the foreground process comes before the uninstall advice")
				assert.Regexp(t, `never (end|advises ending) a process by pid`, row)
				para := row
				if blank := strings.Index(para, "\n\n"); blank >= 0 {
					para = para[:blank]
				}
				assert.NotRegexp(t, `(?i)\b(kill|killall|pkill|taskkill|sigkill)\b`, para, "no kill advice in the row paragraph")
			}
			assert.Positive(t, found, "the doc copy names the row")
		})
	}
}
