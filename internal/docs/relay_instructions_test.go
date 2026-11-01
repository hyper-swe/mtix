// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The FR-21 file relay is experimental and opt-in. An agent that has only the
// generated documents must be able to configure, use, verify and recover it,
// so the managed sections, the sync skill and every mirror carry it, and every
// command, flag, configuration key and doctor row they name exists in the build.

const (
	relayManagedHeading = "## File relay (experimental, opt-in)\n"
	relaySkillHeading   = "## File relay (experimental)\n"
)

var (
	relayManagedFiles = []string{
		"internal/docs/templates/claude.md.tmpl",
		"internal/docs/templates/agents.md.tmpl",
		"docs/AGENTS.md",
		"docs/codex/AGENTS.md",
	}
	relaySkillFiles = []string{
		"internal/docs/templates/skills/sync.md.tmpl",
		".claude-plugin/skills/mtix-sync.md",
		".codex-plugin/skills/sync/SKILL.md",
	}
	relayInvocation = regexp.MustCompile("`mtix sync relay ([a-z-]+)([^`]*)`")
	relayFlagSpan   = regexp.MustCompile("`(--[a-z-]+)[^`]*`")
	relayFlagToken  = regexp.MustCompile(`--[a-z][a-z-]*`)
	relayConfigKey  = regexp.MustCompile(`sync\.relay\.[a-z_]+`)
	relayDoctorRow  = regexp.MustCompile("`(relay [a-z ]+)`")
	relayProviders  = regexp.MustCompile(`(?i)\b(supabase|neon|aurora|rds|cloud sql|alloydb)\b`)
)

func relayRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel))) //nolint:gosec // fixed repo paths
	require.NoError(t, err, rel)
	return string(data)
}

// relaySection returns the text from heading to the next level-two heading.
func relaySection(t *testing.T, rel, heading string) string {
	t.Helper()
	_, rest, found := strings.Cut(relayRepoFile(t, rel), heading)
	require.True(t, found, "%s lacks %q", rel, strings.TrimSpace(heading))
	section, _, _ := strings.Cut(rest, "\n## ")
	return strings.TrimSpace(section)
}

// relayCLIReference returns the relay chapter of the generated CLI reference
// and the subcommand names it lists.
func relayCLIReference(t *testing.T) (string, map[string]bool) {
	t.Helper()
	_, rest, found := strings.Cut(relayRepoFile(t, "docs/CLI_REFERENCE.md"), "\n## relay\n")
	require.True(t, found, "CLI reference has no relay chapter")
	chapter, _, _ := strings.Cut(rest, "\n## repair\n")
	subs := map[string]bool{}
	for _, m := range regexp.MustCompile("(?m)^- `([a-z-]+)[ `]").FindAllStringSubmatch(chapter, -1) {
		subs[m[1]] = true
	}
	require.NotEmpty(t, subs)
	return chapter, subs
}

func assertRelayGuidance(t *testing.T) {
	t.Helper()
	managed := relaySection(t, relayManagedFiles[0], relayManagedHeading)
	for _, rel := range relayManagedFiles {
		assert.Equal(t, managed, relaySection(t, rel, relayManagedHeading), rel)
	}
	skill := relaySection(t, relaySkillFiles[0], relaySkillHeading)
	for _, rel := range relaySkillFiles {
		assert.Equal(t, skill, relaySection(t, rel, relaySkillHeading), rel)
	}
	for _, phrase := range []string{"experimental", "opt-in", "`sync.relay.dir`", "no relay command contacts a hub", "read, print, paste or commit a relay key", "where one exists", "`mtix sync relay tick`", "`mtix sync doctor`", "Never"} {
		assert.Contains(t, managed, phrase)
	}
	for _, phrase := range []string{"experimental and opt-in", "0.6.0", "Hub contact and cost", "never wakes a database that pauses when idle", "print the error alone", "`sync.relay.require_auth=false`", "`sync.hub_contact`", "out of band", "**Configure**", "**Use.**", "**Verify.**", "**Troubleshoot.**", "**Recover.**", "**Never.**"} {
		assert.Contains(t, skill, phrase)
	}
	for _, text := range []string{managed, skill} {
		assert.False(t, relayProviders.MatchString(text), "provider name in relay guidance")
	}
	assertRelayCommandsExist(t, managed+"\n"+skill)
	assertRelayConfigAndDoctorRows(t, managed+"\n"+skill)
}

// assertRelayCommandsExist checks every named relay subcommand and flag
// against the generated CLI reference, which a cmd/mtix test keeps in step
// with the command tree.
func assertRelayCommandsExist(t *testing.T, text string) {
	t.Helper()
	chapter, subs := relayCLIReference(t)
	sections := relaySubcommandSections(chapter)
	found := relayInvocation.FindAllStringSubmatch(text, -1)
	require.NotEmpty(t, found)
	for _, m := range found {
		assert.Truef(t, subs[m[1]], "mtix sync relay %s is not in the build", m[1])
		for _, flag := range relayFlagToken.FindAllString(m[2], -1) {
			assert.Containsf(t, sections[m[1]], "`"+flag+"`", "flag %s is not a flag of mtix sync relay %s", flag, m[1])
		}
	}
	for _, m := range relayFlagSpan.FindAllStringSubmatch(text, -1) {
		assert.Containsf(t, chapter, "`"+m[1]+"`", "flag %s is not in the build", m[1])
	}
}

// relaySubcommandSections splits the relay chapter into one section per
// subcommand, so a flag is checked against the subcommand that names it.
func relaySubcommandSections(chapter string) map[string]string {
	sections := map[string]string{}
	for _, part := range strings.Split(chapter, "\n## ")[1:] {
		name, body, _ := strings.Cut(part, "\n")
		sections[strings.TrimSpace(name)] = body
	}
	return sections
}

func assertRelayConfigAndDoctorRows(t *testing.T, text string) {
	t.Helper()
	keys := relayRepoFile(t, "internal/service/config_keys.go")
	for _, key := range relayConfigKey.FindAllString(text, -1) {
		// The allow-list entries read "key": true; the defaults map does not.
		allowed := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `":\s+true,`)
		assert.Truef(t, allowed.MatchString(keys), "config key %s is not an allowed key in the build", key)
	}
	doctor := relayRepoFile(t, "cmd/mtix/sync_doctor_relay.go")
	for _, m := range relayDoctorRow.FindAllStringSubmatch(text, -1) {
		assert.Containsf(t, doctor, `"`+m[1]+`"`, "doctor row %q is not in the build", m[1])
	}
	assert.Contains(t, doctor, "fix:")
}
