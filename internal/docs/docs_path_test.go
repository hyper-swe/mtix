// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Regression for the user-reported broken references in installed AGENTS.md:
// agents.md.tmpl is a DUAL-PLACEMENT template — the generator renders it into
// .mtix/docs/ (where bare sibling links like WORKFLOWS.md resolve), while the
// codex/pi plugin targets render the same template at the PROJECT ROOT or a
// global agent dir, where every bare link dangled. Cross-doc links now carry
// {{ .DocsPath }}: "" for the in-docs copy, ".mtix/docs/" for installs.
package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refDocs are the generated reference docs AGENTS.md links to.
var refDocs = []string{
	"STATUS_MACHINE.md", "CLI_REFERENCE.md", "WORKFLOWS.md", "CONTEXT_CHAIN.md",
	"BLOCKED_HANDLING.md", "SESSION_PROTOCOL.md", "PATTERNS.md", "TROUBLESHOOTING.md",
	"workflows/solo.md", "workflows/small-team.md", "workflows/safety-critical.md",
}

// bareLinkRe matches a markdown link whose target is one of the reference
// docs with NO path prefix — e.g. "](WORKFLOWS.md)". Correct only when the
// rendered file lives beside them.
func bareLinkRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`\]\(` + regexp.QuoteMeta(name) + `\)`)
}

// TestPluginInstall_AgentsMD_LinksReferenceDotMtixDocs: an AGENTS.md
// installed at the project root must reference every generated doc through
// .mtix/docs/ — a bare sibling link there points at a file that does not
// exist (the reported defect).
func TestPluginInstall_AgentsMD_LinksReferenceDotMtixDocs(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	installer := NewPluginInstaller(projectDir, minimalTemplateData(), nil)
	_, err := installer.Install("codex", false)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(projectDir, "AGENTS.md"))
	require.NoError(t, err)
	s := string(content)

	for _, name := range refDocs {
		if !assert.Contains(t, s, "](.mtix/docs/"+name+")",
			"root-installed AGENTS.md must link %s through .mtix/docs/", name) {
			continue
		}
		assert.NotRegexp(t, bareLinkRe(name), s,
			"root-installed AGENTS.md must not carry a bare dangling link to %s", name)
	}
}

// TestGenerator_AgentsMD_KeepsSiblingLinks: the copy generated INTO
// .mtix/docs/ must keep bare sibling links — prefixing there would break the
// placement that was always correct.
func TestGenerator_AgentsMD_KeepsSiblingLinks(t *testing.T) {
	outDir := t.TempDir()
	g, err := NewGenerator("templates", outDir, minimalTemplateData(), nil)
	require.NoError(t, err)
	_, err = g.Generate(true)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(outDir, "AGENTS.md"))
	require.NoError(t, err)
	s := string(content)

	for _, name := range refDocs {
		assert.Regexp(t, bareLinkRe(name), s,
			"in-docs AGENTS.md must keep the bare sibling link to %s", name)
		assert.NotContains(t, s, "](.mtix/docs/"+name+")",
			"in-docs AGENTS.md must not double-path %s", name)
	}
}
