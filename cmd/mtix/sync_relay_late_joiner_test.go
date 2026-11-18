// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests pin and execute the documented fresh-peer relay workflow (MTIX-263).
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func lateJoinerRepoFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", path))
	require.NoError(t, err)
	return string(raw)
}

func lateJoinerSection(t *testing.T, path string) string {
	t.Helper()
	text := lateJoinerRepoFile(t, path)
	_, section, ok := strings.Cut(text, "**Fresh late joiner (no local tasks).**")
	require.True(t, ok, "missing late-joiner instructions in %s", path)
	section, _, ok = strings.Cut(section, "<!-- END RELAY LATE JOINER -->")
	require.True(t, ok)
	return section
}

// disposableSection returns the USERMANUAL section for peers that lose their store.
func disposableSection(t *testing.T) string {
	t.Helper()
	text := lateJoinerRepoFile(t, "USERMANUAL.md")
	_, section, found := strings.Cut(text, "### Peers whose workspace does not survive between sessions")
	require.True(t, found)
	section, _, found = strings.Cut(section, "### Waiting for inbox events")
	require.True(t, found)
	// The clone-refused blockquote that follows belongs to the recovery text, not to this section.
	section, _, _ = strings.Cut(section, "\n> `mtix sync relay clone` is for a workspace")
	return section
}

func lateJoinerCommands(t *testing.T, path string) []string {
	t.Helper()
	text := lateJoinerRepoFile(t, path)
	require.NotContains(t, text, "A peer that has no store yet then runs")
	for _, stale := range []string{
		"survives a rebuilt workspace",
		"before any `mtix sync relay attach`",
		"do not run `mtix sync relay attach` first",
	} {
		require.NotContains(t, text, stale, "stale wording in %s", path)
	}
	section := lateJoinerSection(t, path)
	for _, phrase := range []string{"Do not run `mtix sync relay attach`", "RELAY_NO_SHARED_PROJECT", "local history", "out of band", "0700", "0600", "never print", "stop any daemon", "mtix sync relay clone --export", "NEW peer id", "RELAY_PUBLISHER_DIVERGED", "no key files to copy"} {
		require.Contains(t, section, phrase, path)
	}
	_, code, ok := strings.Cut(section, "```bash\n")
	require.True(t, ok)
	code, _, ok = strings.Cut(code, "\n```")
	require.True(t, ok)
	commands := strings.Split(code, "\n")
	require.Equal(t, []string{"mtix init <PREFIX>", "mtix config set sync.relay.peer_id <new-peer-id>", "# Copy the relay key epoch files now, as described above.", "mtix config set sync.relay.dir <dir>", "mtix sync relay clone", "mtix sync relay tick"}, commands)
	return commands
}

func TestRelayLateJoiner_Docs_PinProvenSequence(t *testing.T) {
	paths := []string{
		"internal/docs/templates/skills/sync.md.tmpl",
		".claude-plugin/skills/mtix-sync.md",
		".codex-plugin/skills/sync/SKILL.md",
		"USERMANUAL.md",
		"internal/docs/templates/troubleshooting.md.tmpl",
		"docs/TROUBLESHOOTING.md",
	}
	canonical := lateJoinerSection(t, paths[0])
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			lateJoinerCommands(t, path)
			require.Equal(t, canonical, lateJoinerSection(t, path), "late-joiner section must be identical in every copy")
		})
	}
}

func TestRelayLateJoiner_Docs_AttachNotPartOfJoining(t *testing.T) {
	const phrase = "attach is not part of joining, before or after clone"
	for _, path := range []string{
		"internal/docs/templates/skills/sync.md.tmpl",
		".claude-plugin/skills/mtix-sync.md",
		".codex-plugin/skills/sync/SKILL.md",
		"USERMANUAL.md",
	} {
		require.Contains(t, lateJoinerRepoFile(t, path), phrase, path)
	}
}

func TestRelayLateJoiner_Docs_DisposableSectionNeverAttaches(t *testing.T) {
	section := disposableSection(t)
	require.NotContains(t, strings.ToLower(section), "attach", "attach must not appear in the disposable section, prose or code")
	for _, phrase := range []string{
		"NEW peer id",
		"RELAY_PUBLISHER_DIVERGED",
		"mtix sync relay tick",
		"before the store is lost",
		"prune window",
		"mtix sync relay retire-peer <old-peer-id>",
	} {
		require.Contains(t, section, phrase)
	}
	require.NotContains(t, section, "Keep the old project until that verification")
	require.NotContains(t, section, "reuse the old peer id")
}

func lateJoinerSource(t *testing.T) (string, string) {
	t.Helper()
	source, relay := t.TempDir(), t.TempDir()
	mustCLI(t, source, "init", "TEST")
	mustCLI(t, source, "config", "set", "sync.relay.peer_id", rejoinOldPeer)
	mustCLI(t, source, "create", "Snapshot task")
	mustCLI(t, source, "sync", "relay", "init", relay)
	mustCLI(t, source, "config", "set", "sync.relay.dir", relay)
	mustCLI(t, source, "sync", "relay", "tick")
	mustCLI(t, source, "sync", "relay", "clone", "--export")
	return source, relay
}

func TestRelayLateJoiner_EmptyAttach_RefusesWithoutImport(t *testing.T) {
	identityOperatorState(t)
	source, relay := lateJoinerSource(t)
	fresh := t.TempDir()
	mustCLI(t, fresh, "init", "TEST")
	mustCLI(t, fresh, "config", "set", "sync.relay.peer_id", rejoinNewPeer)
	copyRelayKeys(t, source, fresh)
	before := mustCLI(t, fresh, "list", "--json")
	_, stderr, err := cliIn(t, fresh, "sync", "relay", "attach", relay)
	require.Error(t, err)
	require.Contains(t, stderr, "RELAY_NO_SHARED_PROJECT")
	require.Equal(t, before, mustCLI(t, fresh, "list", "--json"))
}

func TestRelayLateJoiner_DocumentedCommands_ExchangeWork(t *testing.T) {
	commands := lateJoinerCommands(t, "internal/docs/templates/skills/sync.md.tmpl")
	identityOperatorState(t)
	source, relay := lateJoinerSource(t)
	fresh := t.TempDir()
	for _, command := range commands {
		if strings.HasPrefix(command, "#") {
			copyRelayKeys(t, source, fresh)
			continue
		}
		args := strings.Fields(command)[1:]
		for i, arg := range args {
			args[i] = strings.NewReplacer("<PREFIX>", "TEST", "<new-peer-id>", rejoinNewPeer, "<dir>", relay).Replace(arg)
		}
		mustCLI(t, fresh, args...)
		if command == "mtix sync relay clone" {
			require.Equal(t, listUIDs(t, mustCLI(t, source, "list", "--json")), listUIDs(t, mustCLI(t, fresh, "list", "--json")))
			// The tick must receive work newer than the snapshot, not just re-import it.
			mustCLI(t, source, "create", "After snapshot")
			mustCLI(t, source, "sync", "relay", "tick")
		}
	}
	require.ElementsMatch(t, listUIDs(t, mustCLI(t, source, "list", "--json")), listUIDs(t, mustCLI(t, fresh, "list", "--json")))
	mustCLI(t, fresh, "create", "Late joiner work")
	require.Positive(t, publishedCount(t, fresh))
	mustCLI(t, source, "sync", "relay", "tick")
	want := listUIDs(t, mustCLI(t, fresh, "list", "--json"))
	require.ElementsMatch(t, want, listUIDs(t, mustCLI(t, source, "list", "--json")))
	for _, dir := range []string{fresh, source} {
		mustCLI(t, dir, "sync", "relay", "tick")
		require.ElementsMatch(t, want, listUIDs(t, mustCLI(t, dir, "list", "--json")), "repeated ticks preserve identities")
	}
}
