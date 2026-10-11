// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests shipped launch guidance and rendered instruction coverage.
package docs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var wakeLaunchRE = regexp.MustCompile(`\b(?:claude\s+(?:-p|--print)|codex\s+exec|agent\s+(?:-p|--print))\b`)
var wakeVariableRE = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z_0-9]*)`)
var wakeCaptureRE = regexp.MustCompile(`^([A-Za-z_][A-Za-z_0-9]*)="\$\(mtix inbox --agent (?:"\$[A-Za-z_][A-Za-z_0-9]*"|[A-Za-z_][A-Za-z_0-9-]*) --format prompt\)"$`)
var wakeInputLineRE = regexp.MustCompile(`^printf '%s\\n' "\$(?:[A-Za-z_][A-Za-z_0-9]*|\{[A-Za-z_][A-Za-z_0-9]*\})" \| (?:claude -p|codex exec -)$`)
var wakeEmptyRE = regexp.MustCompile(`^\[ -z "\$(?:[A-Za-z_][A-Za-z_0-9]*|\{[A-Za-z_][A-Za-z_0-9]*\})" \] && exit 0$`)
var wakeSourceVariableRE = regexp.MustCompile(`([A-Za-z_][A-Za-z_0-9]*)=[^\n]*mtix inbox`)
var wakeShellLineRE = regexp.MustCompile(`^(?:(?:[({!][ \t]*)*(?:[A-Za-z_][A-Za-z_0-9]*=|(?:[A-Za-z0-9_./-]+|"[^"\n]+"|'[^'\n]+'|\$\{?[A-Za-z_][A-Za-z_0-9]*\}?)+[ \t])|[<>])`)
var wakeRawShellLineRE = regexp.MustCompile(`^(?:[({!][ \t]*)*(?:[A-Za-z_][A-Za-z_0-9]*=|(?:[0-9]*[A-Za-z_][A-Za-z_0-9./-]*|[./][A-Za-z_0-9./-]*|"[^"\n]+"|'[^'\n]+')+[ \t])`)
var wakeInlineRE = regexp.MustCompile("`([^`\\n]+)`")

func wakeCommandLines(text string) []string {
	text = strings.ReplaceAll(text, "\\\n", " ")
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		plain := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if wakeRawShellLineRE.MatchString(plain) && strings.Contains(plain, "$") {
			lines = append(lines, plain)
			continue
		}
		inline := wakeInlineRE.FindAllStringSubmatch(line, -1)
		if len(inline) > 0 {
			for _, span := range inline {
				lines = append(lines, span[1])
			}
			remaining := wakeInlineRE.ReplaceAllString(line, "")
			if wakeVariableRE.MatchString(remaining) || strings.Contains(remaining, "$(") {
				lines = append(lines, line)
			}
		} else {
			lines = append(lines, line)
		}
	}
	return lines
}

func wakeLaunchProblems(text string) []string {
	lines := wakeCommandLines(text)
	inputs := map[string]bool{"PAYLOAD": true}
	for _, line := range lines {
		if capture := wakeSourceVariableRE.FindStringSubmatch(line); capture != nil {
			inputs[capture[1]] = true
		}
	}
	var problems []string
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		line = strings.Join(strings.Fields(line), " ")
		if wakeCaptureRE.MatchString(line) || wakeEmptyRE.MatchString(line) || wakeInputLineRE.MatchString(line) {
			continue
		}
		if wakeNeedsInputCheck(line, inputs) {
			problems = append(problems, line)
		}
	}
	return problems
}

func wakeNeedsInputCheck(line string, inputs map[string]bool) bool {
	if wakeReferencesInput(line, inputs) || wakeSourceVariableRE.MatchString(line) {
		return true
	}
	if wakeOtherInputForms[line] {
		return false
	}
	if wakeVariableRE.MatchString(line) || strings.Contains(line, "$(") {
		return wakeShellLineRE.MatchString(line) || strings.ContainsAny(line, "|;") || strings.HasPrefix(line, "[")
	}
	return wakeHasLaunchOperands(line)
}

func wakeReferencesInput(line string, inputs map[string]bool) bool {
	for _, variable := range wakeVariableRE.FindAllStringSubmatch(line, -1) {
		if inputs[variable[1]] {
			return true
		}
	}
	if strings.HasPrefix(line, "export ") {
		for _, word := range strings.Fields(line) {
			if inputs[strings.Trim(word, ";`.")] {
				return true
			}
		}
	}
	return false
}

func wakeHasLaunchOperands(line string) bool {
	if !wakeLaunchRE.MatchString(line) {
		return false
	}
	for _, bare := range []string{"claude -p", "claude --print", "codex exec", "codex exec -", "agent -p", "agent --print"} {
		if line == bare {
			return false
		}
	}
	return true
}

// These finite non-inbox forms describe repository metadata and backup paths.
var wakeOtherInputForms = map[string]bool{
	`> mtix config set sync.relay.peer_id "$(cat /persistent/relay-id)"`:                                  true,
	`> mtix config set sync.relay.dir "$(cat /persistent/relay-dir)"`:                                     true,
	`> mtix sync relay attach "$(cat /persistent/relay-dir)"`:                                             true,
	"The default `\"$user\", public` puts a schema named after the role first":                            true,
	`MTIX_FAULTFS_DIR=$(scripts/faultfs.sh create) go test ./e2e/faultinject/ -tags=faultinject -count=1`: true,
	`if [ -z "${MTIX_BIN:-}" ]; then`:                                                                     true,
	`if ! MTIX_BIN="$(command -v mtix)"; then`:                                                            true,
	`msg="$(git log -1 --format='%B' "${sha}" 2>/dev/null || true)"`:                                      true,
	`hit="$(printf '%s' "${msg}" | grep -Eo "${PROVISIONAL_RE}" || true)"`:                                true,
	`if [ -n "${hit}" ]; then`:                                                                            true,
	`subject="$(git log -1 --format='%s' "${sha}" 2>/dev/null || true)"`:                                  true,
	`printf '%s %s [%s]\n' "${sha}" "${subject}" "$(printf '%s' "${hit}" | tr '\n' ',' | sed 's/,$//')"`:  true,
	`[ -z "${localsha:-}" ] && continue`:                                                                  true,
	`[ "${localsha}" = "${ZERO_SHA}" ] && continue`:                                                       true,
	`if [ "${remotesha:-${ZERO_SHA}}" = "${ZERO_SHA}" ]; then`:                                            true,
	`offenders="$(scan_range_for_provisional "${localsha}" --not --remotes)"`:                             true,
	`offenders="$(scan_range_for_provisional "${remotesha}..${localsha}")"`:                               true,
	`if [ -n "${offenders}" ]; then`:                                                                      true,
	`printf '%s\n' "${offenders}" >&2`:                                                                    true,
	`if [ "${PROVISIONAL_FOUND}" = "1" ]; then`:                                                           true,
	`if [ "${MTIX_BLOCK_PROVISIONAL:-0}" = "1" ]; then`:                                                   true,
	`if [ -z "${MTIX_SYNC_DSN:-}" ] && [ ! -f ".mtix/secrets" ]; then`:                                    true,
	`if ! "${MTIX_BIN}" sync push 2>&1; then`:                                                             true,
	`if [ -f "${TASKS_FILE}" ]; then`:                                                                     true,
	`PRE_HASH="$(git hash-object "${TASKS_FILE}")"`:                                                       true,
	`if ! "${MTIX_BIN}" sync --fix >/dev/null 2>&1; then`:                                                 true,
	`POST_HASH="$(git hash-object "${TASKS_FILE}")"`:                                                      true,
	`if [ "${PRE_HASH}" = "${POST_HASH}" ]; then`:                                                         true,
	`git add -- "${TASKS_FILE}"`:                                                                          true,
	`TIMESTAMP="$(date -u +%Y-%m-%dT%H:%M:%SZ)"`:                                                          true,
	`COMMIT_MSG="chore(snapshot): tasks.json refresh @ ${TIMESTAMP}"`:                                     true,
	`if [ "${MTIX_HOOK_AMEND:-0}" = "1" ]; then`:                                                          true,
	`git commit --quiet --no-verify -m "${COMMIT_MSG}"`:                                                   true,
	`NEW_SHA="$(git rev-parse --short HEAD)"`:                                                             true,
	`printf 'mtix pre-push: sync push + snapshot committed (%s) %s\n' "${NEW_SHA}" "${TASKS_FILE}" >&2`:   true,
	`DATE=$(date -u +%Y%m%dT%H%M%SZ)`:                                                                     true,
	`mtix sync backup --output "/tmp/mtix-hub-${DATE}.sql"`:                                               true,
	`<your-upload-tool> "/tmp/mtix-hub-${DATE}.sql" "<bucket>/mtix-hub/${DATE}.sql" # object storage (an S3-compatible API or similar)`: true,
	`shred -u "/tmp/mtix-hub-${DATE}.sql"`: true,
	`mtix sync backup --output "hub-$(date -u +%Y%m%dT%H%M%SZ).sql" # pg_dump of every mtix hub table`: true,
}

func wakeShippedFiles(t *testing.T) map[string]string {
	t.Helper()
	files := loadShippedDocs(t).all
	for _, path := range []string{"README.md", "CHANGELOG.md"} {
		files[path] = wakeSource(t, path)
	}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(filepath.Join(root, "examples"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	require.NoError(t, err)
	require.Contains(t, files, "examples/hooks/wake-agent.sh")
	require.Contains(t, files, "USERMANUAL.md")
	for _, path := range []string{"README.md", "CHANGELOG.md", "docs/MCP-SETUP.md", "internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md"} {
		require.Contains(t, files, path)
	}
	return files
}

func TestWakeExample_ShippedGuidance_UsesStaticLaunchOperands(t *testing.T) {
	for path, text := range wakeShippedFiles(t) {
		t.Run(path, func(t *testing.T) { require.Empty(t, wakeLaunchProblems(text)) })
	}
}

func TestWakeExample_CommandForms_AreClassifiedIndependently(t *testing.T) {
	tests := []struct {
		text  string
		valid bool
	}{
		{`printf '%s\n' "$INPUT" | claude -p`, true},
		{`# printf '%s\n' "$MESSAGE" | codex exec -`, true},
		{`claude -p "$INPUT"`, false},
		{`claude --print "${OTHER_INPUT}"`, false},
		{`# codex exec "${MESSAGE}"`, false},
		{"codex exec \\\n  \"$OTHER\"", false},
		{"Use `agent -p \"$TEXT\"`.", false},
		{`claude -p 'literal input'`, false},
		{`codex exec "$(cat input)"`, false},
		{`codex exec ordinary-input`, false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) { require.Equal(t, tt.valid, len(wakeLaunchProblems(tt.text)) == 0) })
	}
}

func TestWakeExample_InputForms_FollowDocumentedRoutine(t *testing.T) {
	forms := []string{
		`/usr/bin/printf '%s\n' "$PAYLOAD" | claude -p`,
		`env printf '%s\n' "$INPUT" | claude -p`,
		`echo "$PAYLOAD" | claude -p`,
		`printf "$INPUT\n" | claude -p`,
		`printf '%s\n' "$MESSAGE" | xargs claude -p`,
		`tee "$INPUT" | claude -p`,
		`gemini -p "$PAYLOAD"`,
		`claude "$OTHER"`,
		`codex "$OTHER"`,
		`codex queue --message "$X"`,
		`claude -pv "$OTHER"`,
		`export PAYLOAD; claude -p`,
		`eval 'claude -p "$INPUT"'`,
		`OTHER="$PAYLOAD"; claude -p`,
		`PRODUCER=printf; "$PRODUCER" '%s\n' "$INPUT" | claude -p`,
		`new-runtime --message "$(mtix inbox --agent worker --format prompt)"`,
	}
	for i, form := range forms {
		t.Run(strconv.Itoa(i), func(t *testing.T) { require.NotEmpty(t, wakeLaunchProblems(form), "documented input form") })
	}
}

func TestWakeExample_SnippetForms_IncludeCompleteCommands(t *testing.T) {
	forms := []string{
		`new-runtime -p "$MESSAGE"`,
		`new-runtime "$MESSAGE"`,
		"eval `printf '%s\\n' \"$INPUT\" | claude -p`",
		`TEXT="$(mtix inbox --agent "$AGENT" --format prompt)"` + "\n" + `export TEXT`,
		`printf '%s\n' "$INPUT" | new-runtime`,
		`printf '%s\n' "$(cat input)" | claude -p`,
	}
	wrappers := []string{"%s", "# %s", "Use `%s`.", "```sh\n%s\n```"}
	for i, form := range forms {
		for j, wrapper := range wrappers {
			t.Run(fmt.Sprintf("%d/%d", i, j), func(t *testing.T) {
				require.NotEmpty(t, wakeLaunchProblems(fmt.Sprintf(wrapper, form)), "complete documented form")
			})
		}
	}
	for _, form := range []string{
		`TEXT="$(mtix inbox --agent "$AGENT" --format prompt)"` + "\n" + `[ -z "$TEXT" ] && exit 0` + "\n" + `printf '%s\n' "$TEXT" | claude -p`,
		`# printf '%s\n' "${MESSAGE}" | codex exec -`,
		"printf '%s\\n' \"$INPUT\" \\\n | claude -p",
	} {
		require.Empty(t, wakeLaunchProblems(form), "documented form")
	}
}

func TestWakeExample_CombinedForms_FollowDocumentedRoutine(t *testing.T) {
	forms := []string{
		`new-runtime --profile "batch" -p "$MESSAGE"`,
		`if true; then gemini -p "$MESSAGE"; fi`,
		`/usr/bin/printf '%s\n' "$MESSAGE" | gemini -p`,
		`printf "$MESSAGE\n" | gemini -p`,
	}
	for i, form := range forms {
		t.Run(strconv.Itoa(i), func(t *testing.T) { require.NotEmpty(t, wakeLaunchProblems(form), "complete documented form") })
	}
}

func TestWakeExample_ExecutableForms_FollowDocumentedRoutine(t *testing.T) {
	forms := []string{
		"Use `GEMINI -p \"$MESSAGE\"`.",
		"Use `\"gemini\" -p \"$MESSAGE\"`.",
		"`'gemini' -p \"$MESSAGE\"`",
		"```sh\n(gemini -p \"$MESSAGE\")\n```",
		`# GEMINI -p "$MESSAGE"`,
		"`\"new-runtime\" --message \"$MESSAGE\"`",
	}
	for i, form := range forms {
		t.Run(strconv.Itoa(i), func(t *testing.T) { require.NotEmpty(t, wakeLaunchProblems(form), "complete documented form") })
	}
}

func TestWakeExample_CompoundForms_KeepCompleteInputChecks(t *testing.T) {
	forms := []string{
		`MiXeD-runtime -p "$UNSEEN"`,
		`"ge"mini -p "$UNSEEN"`,
		`! ('gemini' -p "$UNSEEN")`,
		`{ GEMINI -p "$UNSEEN"; }`,
		`$EXECUTABLE -p "$UNSEEN"`,
		`>result "gemini" -p "$UNSEEN"`,
		"GEMINI `printf '%s\\n' \"$UNSEEN\" | claude -p`",
	}
	for i, form := range forms {
		t.Run(strconv.Itoa(i), func(t *testing.T) { require.NotEmpty(t, wakeLaunchProblems(form), "complete documented form") })
	}
	require.Empty(t, wakeLaunchProblems("The default `\"$user\", public` puts a schema named after the role first"), "metadata form")
}

func TestWakeExample_MetadataForms_KeepInputChecks(t *testing.T) {
	for _, form := range []string{
		`git add -- "${TASKS_FILE}"`,
		`if [ -n "${hit}" ]; then`,
		`printf '%s\n' "${offenders}" >&2`,
		`claude mcp add mtix -- mtix mcp -C /path/to/project`,
	} {
		require.Empty(t, wakeLaunchProblems(form), "metadata form")
	}
	for i, form := range []string{
		`git add -- "$MESSAGE"`,
		`if [ -n "$MESSAGE" ]; then gemini -p "$MESSAGE"; fi`,
		`printf "$MESSAGE\n" >&2`,
		`claude mcp add "$MESSAGE"`,
		`TASKS_FILE="$(mtix inbox --agent worker --format prompt)"` + "\n" + `git add -- "${TASKS_FILE}"`,
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) { require.NotEmpty(t, wakeLaunchProblems(form), "documented input form") })
	}
}

func TestWakeExample_GeneratedGuidance_DescribesRoutine(t *testing.T) {
	paths := []string{"internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md"}
	for _, path := range paths {
		text := wakeSource(t, path)
		for _, term := range []string{"standard input", "empty", "ack", "wake-agent.sh"} {
			require.Contains(t, text, term, path)
		}
	}
	for _, path := range []string{"internal/docs/templates/agents.md.tmpl", "internal/docs/templates/claude.md.tmpl"} {
		require.Contains(t, wakeSource(t, path), "wake-agent.sh", path)
	}
}

func wakeRoutine(t *testing.T, text string) string {
	t.Helper()
	parts := strings.SplitN(text, "## Reference Wake Routine\n", 2)
	require.Len(t, parts, 2)
	return strings.TrimSpace(strings.SplitN(parts[1], "<!-- END AUTO-GENERATED -->", 2)[0])
}

func TestWakeExample_RenderedGuidance_MatchesShippedRoutine(t *testing.T) {
	out := t.TempDir()
	data := minimalTemplateData()
	installer := NewPluginInstaller(out, data, nil)
	_, err := installer.Install("claude-code", false)
	require.NoError(t, err)
	generated, err := os.ReadFile(filepath.Join(out, ".claude", "skills", "mtix-multi-agent.md"))
	require.NoError(t, err)
	want := wakeRoutine(t, string(generated))
	for _, path := range []string{".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md"} {
		require.Equal(t, want, wakeRoutine(t, wakeSource(t, path)), path)
	}
	_, err = installer.Install("codex", false)
	require.NoError(t, err)
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		b, err := os.ReadFile(filepath.Join(out, name))
		require.NoError(t, err)
		require.Contains(t, string(b), "standard-input launch routine")
	}
}

func TestWakeExample_ManagedGuidance_RefreshesExistingSection(t *testing.T) {
	out := t.TempDir()
	gen, err := NewEmbeddedGenerator(out, minimalTemplateData(), nil)
	require.NoError(t, err)
	for _, name := range []string{"agents", "claude"} {
		file := strings.ToUpper(name) + ".md"
		require.NoError(t, os.WriteFile(filepath.Join(out, file), []byte("User notes\n<!-- AUTO-GENERATED: WAKE_ROUTINE -->\nPrevious routine\n<!-- END AUTO-GENERATED -->\n"), 0o600))
		_, err = gen.generateFile(DocFile{Name: file, TemplateName: name + ".md.tmpl", Type: TemplateBased}, true)
		require.NoError(t, err)
		b, err := os.ReadFile(filepath.Join(out, file))
		require.NoError(t, err)
		require.Contains(t, string(b), "User notes")
		require.Contains(t, string(b), "standard-input launch routine")
		require.NotContains(t, string(b), "Previous routine")
	}
}
