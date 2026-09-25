// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPluginInstaller_AdminSkill_MatchesPluginMirror: the admin skill in
// .claude-plugin/skills is byte-identical to the admin skill template
// rendered for this repository's prefix (MTIX), so the plugin ships what
// the template says (MTIX-95.1.4).
func TestPluginInstaller_AdminSkill_MatchesPluginMirror(t *testing.T) {
	data := minimalTemplateData()
	data.ProjectPrefix = "MTIX"
	dir := t.TempDir()
	_, err := NewPluginInstaller(dir, data, nil).Install("claude-code", false)
	require.NoError(t, err)
	rendered, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", "mtix-admin.md")) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	mirror, err := os.ReadFile(filepath.Join("..", "..", ".claude-plugin", "skills", "mtix-admin.md"))
	require.NoError(t, err)
	require.Equal(t, string(rendered), string(mirror), ".claude-plugin/skills/mtix-admin.md matches the rendered template")
}

// TestPluginInstaller_AdminSkill_StatesHubPrivilegeCoverage: the rendered
// admin skill states when strict mode fails, what superuser_membership
// covers, that the REPLICATION role attribute is outside the check, and
// the missing schema USAGE cause of a backup that finds no hub table
// (MTIX-95.1.4).
func TestPluginInstaller_AdminSkill_StatesHubPrivilegeCoverage(t *testing.T) {
	dir := t.TempDir()
	_, err := NewPluginInstaller(dir, minimalTemplateData(), nil).Install("claude-code", false)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", "mtix-admin.md")) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	skill := string(body)
	for _, phrase := range []string{
		"Setting it turns on strict mode for the doctor's `hub-privileges` check, which then fails whenever " +
			"`mtix sync harden` would report a finding, or when the check cannot run",
		"(its `hub-privileges` check then fails, instead of warning, whenever `mtix sync harden` would report a " +
			"finding, or when the check cannot run)",
		"`superuser_membership` (a role that can SET ROLE to a superuser, or that holds ADMIN OPTION on a role that can)",
		"The REPLICATION role attribute is outside the check too: a role that has it is checked for its privileges " +
			"and memberships like any other role, but not for the attribute.",
		"If the DSN's role lacks USAGE on the hub's schema, which leaves that schema off its search_path, run the " +
			"GRANT statements the backup prints: the schema's owner grants USAGE on the schema (",
		"and the table owner grants SELECT on each sync table and sync-table sequence by name",
	} {
		require.Contains(t, skill, phrase)
	}
	require.NotContains(t, skill, "when any other role can use the sync tables", "the narrower strict-mode description is gone")
	require.NotContains(t, skill, "ALL TABLES IN SCHEMA", "the grant names each sync table")
}

// markRestoredSentence is the one sentence every document uses for what the
// least-privilege list means for mtix sync mark-restored (MTIX-95.1.4).
const markRestoredSentence = "A syncing role set up with the least-privilege list holds no UPDATE on " +
	"`sync_hub_state`, so it cannot run `mtix sync mark-restored`, which runs as the table owner."

// leastPrivilegePointer says where the least-privilege list is.
const leastPrivilegePointer = "The least-privilege list is in step 2 of the small-team workflow " +
	"(`.mtix/docs/workflows/small-team.md`) and in `docs/SECURITY-MODEL.md`."

// recorderSentence is the one sentence every document uses for how a
// syncing role records restore collisions (MTIX-95.1.7).
const recorderSentence = "A syncing role records restore collisions only through the hub function " +
	"`record_restore_collision`, which runs as the table owner and records a collision only when the " +
	"hub's own data shows an earlier-epoch create holding the number, so the role needs EXECUTE on that " +
	"function and no INSERT on `sync_node_collisions`."

// upgradeOrderSentence is the one sentence every document uses for the
// order of the upgrade steps of an existing hub (MTIX-95.1.7).
const upgradeOrderSentence = "Upgrade every syncing client first, then run the REVOKE statements; if the " +
	"REVOKE comes first, an older client's push that meets a restore collision fails until that client upgrades."

// restoreGrantsSentence is the restore runbook step that gives the syncing
// roles their privileges back (MTIX-95.1.7).
const restoreGrantsSentence = "A dump holds no privileges: after `mtix sync init`, grant each syncing role " +
	"the least-privilege list again, EXECUTE on `record_restore_collision` included, then run " +
	"`mtix sync doctor` with a syncing role's DSN."

// everyLeastPrivilegeCopy returns, by name, the whitespace-normalized text
// of every document that states the least-privilege sentences: the user
// manual, the admin skill (rendered, and its plugin mirrors), the
// small-team workflow and the security model (MTIX-95.1.4).
func everyLeastPrivilegeCopy(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	_, err := NewPluginInstaller(dir, minimalTemplateData(), nil).Install("claude-code", false)
	require.NoError(t, err)
	docs := map[string]string{"rendered admin skill": filepath.Join(dir, ".claude", "skills", "mtix-admin.md")}
	for _, rel := range []string{"USERMANUAL.md", ".claude-plugin/skills/mtix-admin.md",
		".codex-plugin/skills/admin/SKILL.md", "internal/docs/templates/workflows/small-team.md.tmpl",
		"docs/SECURITY-MODEL.md"} {
		docs[rel] = filepath.Join(append([]string{"..", ".."}, strings.Split(rel, "/")...)...)
	}
	texts := map[string]string{}
	for name, path := range docs {
		body, err := os.ReadFile(path) //nolint:gosec // repository and t.TempDir() paths
		require.NoError(t, err)
		texts[name] = strings.Join(strings.Fields(string(body)), " ")
	}
	return texts
}

// TestDocs_MarkRestoredSentence_SameInEveryCopy: the user manual, the
// admin skill (rendered, and its plugin mirrors), the small-team workflow
// and the security model state the same mark-restored sentence, and the
// user manual and the admin skill say where the least-privilege list is
// (MTIX-95.1.4).
func TestDocs_MarkRestoredSentence_SameInEveryCopy(t *testing.T) {
	pointed := map[string]bool{"rendered admin skill": true, "USERMANUAL.md": true,
		".claude-plugin/skills/mtix-admin.md": true, ".codex-plugin/skills/admin/SKILL.md": true}
	for name, text := range everyLeastPrivilegeCopy(t) {
		require.Containsf(t, text, markRestoredSentence, "%s states the mark-restored sentence", name)
		if pointed[name] {
			require.Containsf(t, text, leastPrivilegePointer, "%s says where the list is", name)
		}
	}
}

// TestDocs_RecorderSentence_SameInEveryCopy: every copy states the same
// sentence on how a syncing role records restore collisions, the GRANT an
// owner runs for a syncing role on a hub that mtix sync init has just given
// the function, and the order of the upgrade steps (MTIX-95.1.7).
func TestDocs_RecorderSentence_SameInEveryCopy(t *testing.T) {
	for name, text := range everyLeastPrivilegeCopy(t) {
		require.Containsf(t, text, recorderSentence, "%s states the recorder sentence", name)
		require.Containsf(t, text, "GRANT EXECUTE ON FUNCTION record_restore_collision TO <role>;",
			"%s gives the grant for an existing hub", name)
		require.Containsf(t, text, upgradeOrderSentence, "%s states the upgrade order", name)
	}
}

// TestDocs_RestoreRunbook_GrantsLeastPrivilegeListAgain: the user manual and
// the admin skill (rendered, and its plugin mirrors) carry the restore step
// that grants the syncing roles the least-privilege list again and checks
// it with the doctor; the security model says what a least-privilege role
// cannot do (MTIX-95.1.7).
func TestDocs_RestoreRunbook_GrantsLeastPrivilegeListAgain(t *testing.T) {
	texts := everyLeastPrivilegeCopy(t)
	for _, name := range []string{"USERMANUAL.md", "rendered admin skill", ".claude-plugin/skills/mtix-admin.md",
		".codex-plugin/skills/admin/SKILL.md"} {
		require.Containsf(t, texts[name], restoreGrantsSentence, "%s carries the restore grants step", name)
	}
	require.Contains(t, texts["docs/SECURITY-MODEL.md"],
		"it cannot advance the epoch, set an event's epoch, or insert a collision row")
}

// exemptionSentence is how every document states whom the doctor's check
// of the collision privileges leaves out (MTIX-95.1.7).
const exemptionSentence = "The check skips the table owner, roles that inherit it, and superusers."

// TestDocs_CollisionCheckExemptions_SameInEveryCopy: the user manual, the
// admin skill (rendered, and its plugin mirrors) and the small-team
// workflow state the exemptions of the schema current check's collision
// privileges exactly (MTIX-95.1.7).
func TestDocs_CollisionCheckExemptions_SameInEveryCopy(t *testing.T) {
	for name, text := range everyLeastPrivilegeCopy(t) {
		if name == "docs/SECURITY-MODEL.md" {
			continue
		}
		require.Containsf(t, text, exemptionSentence, "%s states the exemptions", name)
	}
}

// Phrases every copy of the schema current check's description carries
// (MTIX-95.1.7): ownership of the tables' schema, reach through chains of
// SET ROLE and ADMIN OPTION, create events stamped outside the hub's
// epoch range, and how mtix sync init creates migration 017's functions.
const (
	schemaOwnerPhrase = "owns the schema that holds the sync tables"
	reachPhrase       = "can reach, through a chain of SET ROLE and ADMIN OPTION, a role that holds either or " +
		"owns that schema, or a superuser it can then SET ROLE to"
	stampsPhrase = "stamped with a restore epoch below 0 or above the hub's current epoch"
	notEarlier   = "restore-collision checks treat each as not earlier than the current epoch"
	initPhrase   = "creates the restore-collision functions of migration 017 without EXECUTE for PUBLIC"
)

// TestDocs_CollisionCheckReachAndStamps_SameInEveryCopy: the user manual
// and the admin skill (rendered, and its plugin mirrors) describe the
// schema current check with the same phrases: schema ownership, chains of
// SET ROLE and ADMIN OPTION, and create events stamped outside the hub's
// epoch range; the admin skill says mtix sync init creates migration 017's
// functions without EXECUTE for PUBLIC (MTIX-95.1.7).
func TestDocs_CollisionCheckReachAndStamps_SameInEveryCopy(t *testing.T) {
	texts := everyLeastPrivilegeCopy(t)
	for _, name := range []string{"USERMANUAL.md", "rendered admin skill", ".claude-plugin/skills/mtix-admin.md",
		".codex-plugin/skills/admin/SKILL.md"} {
		for _, phrase := range []string{schemaOwnerPhrase, reachPhrase, stampsPhrase, notEarlier} {
			require.Containsf(t, texts[name], phrase, "%s describes the check", name)
		}
		require.NotContainsf(t, texts[name], "can SET ROLE to or administers a role", "%s", name)
		if name != "USERMANUAL.md" {
			require.Containsf(t, texts[name], initPhrase, "%s says how init creates the functions", name)
			require.NotContainsf(t, texts[name], "executable by their owner alone", "%s", name)
		}
	}
}
