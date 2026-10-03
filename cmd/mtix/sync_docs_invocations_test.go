// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/docs"
	"github.com/hyper-swe/mtix/internal/mcp"
)

// The sync documentation text must name only commands and flags this build
// has (MTIX-95.8.1). The command tree is this package's, so the check
// lives here rather than in internal/docs.

var (
	fencedBlockRE = regexp.MustCompile("(?s)```([a-z]*)\n(.*?)```")
	inlineSpanRE  = regexp.MustCompile("`([^`\n]+)`")
	mtixCallRE    = regexp.MustCompile(`(?:^|\s)mtix\s+([^;|&#\n]+)`)
	plainWordRE   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// mtixInvocations returns every `mtix ...` invocation written in a code
// span or a (non-SQL) code block of text, as its argument tokens.
func mtixInvocations(text string) [][]string {
	var snippets []string
	rest := fencedBlockRE.ReplaceAllStringFunc(text, func(block string) string {
		m := fencedBlockRE.FindStringSubmatch(block)
		if m[1] != "sql" {
			snippets = append(snippets, strings.Split(m[2], "\n")...)
		}
		return "\n"
	})
	for _, m := range inlineSpanRE.FindAllStringSubmatch(rest, -1) {
		snippets = append(snippets, m[1])
	}
	var calls [][]string
	for _, s := range snippets {
		for _, m := range mtixCallRE.FindAllStringSubmatch(s, -1) {
			calls = append(calls, strings.Fields(m[1]))
		}
	}
	return calls
}

// invocationProblem returns why args (the tokens after `mtix`) do not
// parse against root, or "" when they do: every command word names a
// command, and every long flag is one the command or a parent defines.
func invocationProblem(root *cobra.Command, args []string) string {
	cmd := root
	i := 0
	for ; i < len(args); i++ {
		var next *cobra.Command
		for _, c := range cmd.Commands() {
			if c.Name() == args[i] || c.HasAlias(args[i]) {
				next = c
				break
			}
		}
		if next == nil {
			break
		}
		cmd = next
	}
	if i < len(args) && cmd.HasSubCommands() && plainWordRE.MatchString(args[i]) {
		return "unknown command " + cmd.CommandPath() + " " + args[i]
	}
	for _, tok := range args[i:] {
		if !strings.HasPrefix(tok, "--") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(tok, "--"), "=")
		if name != "" && cmd.Flag(name) == nil {
			return cmd.CommandPath() + " has no flag --" + name
		}
	}
	return ""
}

// invocationProblems lists every invocation of text that does not parse.
func invocationProblems(root *cobra.Command, text string) []string {
	var out []string
	for _, call := range mtixInvocations(text) {
		if p := invocationProblem(root, call); p != "" {
			out = append(out, "`mtix "+strings.Join(call, " ")+"`: "+p)
		}
	}
	sort.Strings(out)
	return out
}

// TestSyncDocsInvocations_CheckerRejectsUnknownCommandsAndFlags pins the
// checker: it passes real invocations and fails an unknown command, an
// unknown flag and a flag on the wrong command.
func TestSyncDocsInvocations_CheckerRejectsUnknownCommandsAndFlags(t *testing.T) {
	root := newRootCmd()
	good := "Run `mtix sync push`, then `MTIX_SYNC_HOOK=1 mtix sync pull`; `mtix sync doctor --json` and " +
		"`mtix sync backup --output <file>`.\n```bash\nmtix sync repair --status\n```\n"
	require.Empty(t, invocationProblems(root, good))
	require.Len(t, mtixInvocations(good), 5)
	for _, bad := range []string{
		"`mtix sync frobnicate`", "`mtix sync push --no-such-flag`", "`mtix sync status --apply`",
		"```bash\nmtix sync repair --status --nonsense\n```",
	} {
		require.NotEmptyf(t, invocationProblems(root, bad), "%s is rejected", bad)
	}
	require.Empty(t, invocationProblems(root, "```sql\nGRANT mtix sync nonsense;\n```"), "SQL blocks are not scanned")
	require.Empty(t, invocationProblems(root, "the `.mtix/secrets` file and `mtix-sync`"), "paths are not invocations")
}

// TestSyncDocs_EveryMtixInvocation_ExistsInThisBuild: every `mtix ...`
// invocation in the managed SYNC sections of the generated CLAUDE.md and
// AGENTS.md and in the mtix-sync skill, rendered, parses against this
// build's command tree (MTIX-95.8.1).
func TestSyncDocs_EveryMtixInvocation_ExistsInThisBuild(t *testing.T) {
	root := newRootCmd()
	data := docs.BuildTemplateData(root, mcp.NewToolRegistry(), "MTIX", "test")
	data.SyncHubConfigured = true

	docsDir := t.TempDir()
	gen, err := docs.NewEmbeddedGenerator(docsDir, data, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	_, err = gen.Generate(true)
	require.NoError(t, err)
	section := regexp.MustCompile(`(?s)<!-- AUTO-GENERATED: SYNC -->(.*?)<!-- END AUTO-GENERATED -->`)
	scanned := 0
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		body, rerr := os.ReadFile(filepath.Join(docsDir, name)) //nolint:gosec // path from t.TempDir()
		require.NoError(t, rerr)
		m := section.FindStringSubmatch(string(body))
		require.NotNilf(t, m, "%s has the SYNC section", name)
		require.Empty(t, invocationProblems(root, m[1]), name)
		scanned += len(mtixInvocations(m[1]))
	}

	projectDir := t.TempDir()
	_, err = docs.NewPluginInstaller(projectDir, data, slog.New(slog.DiscardHandler)).Install("claude-code", false)
	require.NoError(t, err)
	skill, err := os.ReadFile(filepath.Join(projectDir, ".claude", "skills", "mtix-sync.md")) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	require.Empty(t, invocationProblems(root, string(skill)), "the mtix-sync skill")
	scanned += len(mtixInvocations(string(skill)))
	require.Greater(t, scanned, 40, "the scan reaches the invocations of the new text")
}
