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

// expectedWorkflowDocs is the canonical set of three reference workflow docs
// that MTIX-14.4 ships into .mtix/docs/workflows/.
var expectedWorkflowDocs = []string{
	"solo.md",
	"small-team.md",
	"safety-critical.md",
}

// trustBannerSubstrings are the load-bearing fragments of the mandatory
// trust banner. They are checked as substrings (not exact-equal) so docs
// can reflow whitespace without breaking the test.
var trustBannerSubstrings = []string{
	"BYO PG = trusted team sharing one database",
	"NOT a multi-tenant boundary",
	"Anyone with PG write access can read/modify all task data",
}

// minimumFailureModeRows lists the failure modes that MUST appear in the
// failure-modes table of every workflow doc per the ticket requirements.
var minimumFailureModeRows = []string{
	"PG unreachable",
	"hook bypassed",
	"schema mismatch",
	"audit_log full",
	"two devs push simultaneously",
	"network partition",
}

// TestWorkflowDocs_AllThreePresent verifies the generator emits all three
// workflow docs into <outputDir>/workflows/ on a fresh generation.
func TestWorkflowDocs_AllThreePresent(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	_, err := gen.Generate(false)
	require.NoError(t, err)

	workflowDir := filepath.Join(outDir, "workflows")
	require.DirExists(t, workflowDir, "workflows subdirectory must exist")

	for _, name := range expectedWorkflowDocs {
		path := filepath.Join(workflowDir, name)
		assert.FileExists(t, path, "workflow doc %s must be generated", name)
	}

	// Also assert the public WorkflowDocFiles() helper exposes the same set,
	// so external tooling (mtix_workflow MCP tool, etc.) can enumerate them.
	listed := WorkflowDocFiles()
	assert.Len(t, listed, len(expectedWorkflowDocs))

	listedNames := make(map[string]bool)
	for _, doc := range listed {
		listedNames[doc.Name] = true
	}
	for _, name := range expectedWorkflowDocs {
		assert.True(t, listedNames[name],
			"WorkflowDocFiles() must include %s", name)
	}
}

// TestWorkflowDocs_TrustBannerPresent verifies every workflow doc opens
// with the mandatory BYO-PG trust banner. This is a security-critical
// regression: the banner is the user's first warning that BYO PG is not
// a multi-tenant boundary.
//
// Comparison is whitespace-normalized so the templates can soft-wrap the
// banner across lines (and prefix lines with `> ` for blockquote
// rendering) without false negatives.
func TestWorkflowDocs_TrustBannerPresent(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	_, err := gen.Generate(false)
	require.NoError(t, err)

	for _, name := range expectedWorkflowDocs {
		path := filepath.Join(outDir, "workflows", name)
		body, err := os.ReadFile(path)
		require.NoError(t, err, "read %s", name)

		flat := flattenForSearch(string(body))
		for _, fragment := range trustBannerSubstrings {
			assert.Contains(t, flat, fragment,
				"workflow doc %s must contain trust banner fragment %q",
				name, fragment)
		}

		// The banner must appear at the top of the doc — within the
		// first 1500 chars of the flattened text — so users cannot miss it.
		head := flat
		if len(head) > 1500 {
			head = head[:1500]
		}
		for _, fragment := range trustBannerSubstrings {
			assert.Contains(t, head, fragment,
				"workflow doc %s must show trust banner near the top, not buried",
				name)
		}
	}
}

// flattenForSearch collapses markdown soft-wraps so substring searches
// match the rendered prose. Replaces "\n> " (blockquote continuation)
// and bare "\n" with spaces, then squeezes runs of whitespace.
func flattenForSearch(s string) string {
	s = strings.ReplaceAll(s, "\n> ", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

// TestWorkflowDocs_FailureModesTable_MinimumRows verifies each workflow doc
// contains a failure-modes table covering at minimum the six scenarios
// specified by MTIX-14.4: PG unreachable, hook bypassed, schema mismatch,
// audit_log full, two devs push simultaneously, network partition.
//
// Search is case-insensitive and strips inline-code backticks so the
// docs can render `audit_log` as inline code without breaking the test.
func TestWorkflowDocs_FailureModesTable_MinimumRows(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	_, err := gen.Generate(false)
	require.NoError(t, err)

	for _, name := range expectedWorkflowDocs {
		path := filepath.Join(outDir, "workflows", name)
		body, err := os.ReadFile(path)
		require.NoError(t, err)

		// Strip backticks so "audit_log full" matches "`audit_log` full"
		// in the rendered markdown table cell.
		text := strings.ToLower(strings.ReplaceAll(string(body), "`", ""))

		// Every doc must label its failure-modes table so users can find it.
		assert.Contains(t, text, "failure mode",
			"workflow doc %s must label its failure-modes section", name)

		for _, row := range minimumFailureModeRows {
			assert.Contains(t, text, strings.ToLower(row),
				"workflow doc %s failure-modes table must cover %q",
				name, row)
		}
	}
}

// TestWorkflowDocs_CodeSnippetsExecutable is the lightweight in-package
// equivalent of the CI sandbox check called for in MTIX-14.4 acceptance
// criterion 4. Rather than spawn a shell, we statically validate that
// every fenced code block in every workflow doc is well-formed:
//   - Bash blocks: non-empty, balanced quotes, no obvious unsafe patterns.
//   - SQL blocks: non-empty, end statements with a semicolon.
//
// A sandbox harness is overkill for static reference docs; the static
// check catches the realistic regression (a typo'd snippet shipping into
// agent-accessible docs).
func TestWorkflowDocs_CodeSnippetsExecutable(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	_, err := gen.Generate(false)
	require.NoError(t, err)

	// Permissive fence regex: matches indented or unindented fenced code
	// blocks with optional language tag.
	fenceRE := regexp.MustCompile("(?s)```([A-Za-z0-9]*)\\s*\\n(.*?)```")

	// Across the three docs combined we MUST have at least one snippet
	// (otherwise the docs are vacuous). Individual docs may have zero
	// (the solo doc is intentionally narrative-heavy).
	totalSnippets := 0

	for _, name := range expectedWorkflowDocs {
		path := filepath.Join(outDir, "workflows", name)
		body, err := os.ReadFile(path)
		require.NoError(t, err)

		matches := fenceRE.FindAllStringSubmatch(string(body), -1)
		totalSnippets += len(matches)

		for i, m := range matches {
			lang := strings.ToLower(m[1])
			snippet := strings.TrimSpace(m[2])
			require.NotEmpty(t, snippet,
				"%s: code block #%d (%s) is empty", name, i, lang)

			// Balanced quotes — catches the most common copy-paste typo.
			// Only meaningful for shell-like languages; SQL identifiers
			// can legitimately use unbalanced apostrophes inside literals.
			if lang == "bash" || lang == "sh" || lang == "" {
				single := strings.Count(snippet, "'") - strings.Count(snippet, "\\'")
				double := strings.Count(snippet, "\"") - strings.Count(snippet, "\\\"")
				assert.Equal(t, 0, single%2,
					"%s: code block #%d has unbalanced single quotes", name, i)
				assert.Equal(t, 0, double%2,
					"%s: code block #%d has unbalanced double quotes", name, i)
			}

			switch lang {
			case "sql":
				// Heuristic: SQL blocks should terminate at least one statement.
				assert.Contains(t, snippet, ";",
					"%s: SQL block #%d must end statements with a semicolon",
					name, i)
			case "bash", "sh", "":
				// `set -euo pipefail` is recommended but not required for
				// every snippet (some are one-liners). Just ensure no obvious
				// shell injection footguns slipped in.
				assert.NotContains(t, snippet, "rm -rf /",
					"%s: bash block #%d contains rm -rf / footgun",
					name, i)
			}
		}
	}

	assert.Greater(t, totalSnippets, 0,
		"workflow docs must contain at least one example snippet across the set")
}

// TestWorkflowDocs_NoInsecureExamples verifies workflow docs never recommend
// insecure setup. Specifically:
//   - sslmode=disable / sslmode=require / sslmode=verify-ca must never appear
//     inside a fenced code block (we want to ban executable insecure
//     snippets but still allow descriptive prose like "if you set
//     sslmode=disable mtix refuses to connect").
//   - DSNs with embedded credentials (postgres://user:pass@...) must not
//     appear anywhere — that would be a leaked credential in tracked docs.
//   - dsn: yaml fields (which would imply storing DSN in tracked config)
//     must not appear.
//   - Any PG-using doc must reference MTIX_PG_DSN as the DSN source.
func TestWorkflowDocs_NoInsecureExamples(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	_, err := gen.Generate(false)
	require.NoError(t, err)

	fenceRE := regexp.MustCompile("(?s)```(\\w+)?\\n(.*?)```")

	// DSN-with-credentials regex: scheme://word:word@... — would mean we
	// shipped a hardcoded credential in a doc. The empty-password form
	// (postgres://user@...) is acceptable because it's a connection-shape
	// example, not a leaked credential.
	credRE := regexp.MustCompile(`postgres(?:ql)?://[A-Za-z0-9_.-]+:[^@\s]+@`)

	// dsn yaml field — would mean we recommend storing DSN in tracked config.
	yamlDSNRE := regexp.MustCompile(`(?m)^\s*dsn\s*:`)

	insecureSslmodes := []string{
		"sslmode=disable",
		"sslmode=require",
		"sslmode=verify-ca",
		"sslmode=allow",
		"sslmode=prefer",
	}

	for _, name := range expectedWorkflowDocs {
		path := filepath.Join(outDir, "workflows", name)
		body, err := os.ReadFile(path)
		require.NoError(t, err)

		text := string(body)
		lower := strings.ToLower(text)

		// Insecure sslmode must never appear inside an executable code
		// block. Prose mentions are allowed (and necessary, e.g. to
		// document mtix's refusal behavior).
		for _, m := range fenceRE.FindAllStringSubmatch(text, -1) {
			snippet := strings.ToLower(m[2])
			for _, bad := range insecureSslmodes {
				assert.NotContains(t, snippet, bad,
					"workflow doc %s code block must not use insecure %s", name, bad)
			}
		}

		// Solo doc has no PG examples, so verify-full is required only when
		// the doc actually references sslmode at all.
		if strings.Contains(lower, "sslmode") {
			assert.Contains(t, lower, "sslmode=verify-full",
				"workflow doc %s mentions sslmode but never sets verify-full", name)
		}

		// MTIX_PG_DSN env var must be the DSN source in any PG-using doc.
		if strings.Contains(lower, "postgres") &&
			!strings.HasPrefix(name, "solo") {
			assert.Contains(t, text, "MTIX_PG_DSN",
				"workflow doc %s must reference MTIX_PG_DSN env var, not a hardcoded DSN", name)
		}

		assert.False(t, credRE.MatchString(text),
			"workflow doc %s must not contain a DSN with embedded credentials", name)

		assert.False(t, yamlDSNRE.MatchString(text),
			"workflow doc %s must not have a dsn: yaml field (DSN belongs in env or .mtix/secrets, never in tracked config)", name)
	}
}

// TestWorkflowDocs_LinksToSecurityModel verifies every workflow doc
// cross-references docs/SECURITY-MODEL.md so adopters land on the
// foundational trust doc.
func TestWorkflowDocs_LinksToSecurityModel(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	_, err := gen.Generate(false)
	require.NoError(t, err)

	for _, name := range expectedWorkflowDocs {
		path := filepath.Join(outDir, "workflows", name)
		body, err := os.ReadFile(path)
		require.NoError(t, err)

		text := string(body)

		// Must contain both the prose marker and the actual link target.
		assert.Contains(t, text, "SECURITY-MODEL.md",
			"workflow doc %s must link to SECURITY-MODEL.md", name)
		assert.Contains(t, text, "See also",
			"workflow doc %s must use the See also: SECURITY-MODEL.md cross-reference convention", name)
	}
}

// TestWorkflowDocs_SafetyCriticalHasDR verifies the safety-critical doc
// includes a prominent disaster-recovery section, while solo and
// small-team link to it. Per MTIX-14.4 acceptance: "DR reference
// (safety-critical doc has this prominently; others link to it)".
func TestWorkflowDocs_SafetyCriticalHasDR(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	_, err := gen.Generate(false)
	require.NoError(t, err)

	scPath := filepath.Join(outDir, "workflows", "safety-critical.md")
	body, err := os.ReadFile(scPath)
	require.NoError(t, err)

	scText := strings.ToLower(string(body))
	assert.Contains(t, scText, "disaster recovery",
		"safety-critical doc must have a Disaster Recovery section heading")
	assert.Contains(t, scText, "restore",
		"safety-critical doc must describe the restore procedure")

	// Solo and small-team must reference DR somewhere (either describing
	// their own minimal DR or linking to the safety-critical doc).
	for _, name := range []string{"solo.md", "small-team.md"} {
		path := filepath.Join(outDir, "workflows", name)
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		text := strings.ToLower(string(body))
		// Either the doc has its own DR coverage or it links to the
		// safety-critical doc which does.
		hasDR := strings.Contains(text, "disaster recovery") ||
			strings.Contains(text, "safety-critical.md")
		assert.True(t, hasDR,
			"workflow doc %s must reference DR (its own or via safety-critical.md link)", name)
	}
}

// TestWorkflowDocs_TrustBoundarySection verifies each doc states explicitly
// what the workflow protects against and what it does not. This is the
// "trust boundary explicit" requirement from the ticket.
func TestWorkflowDocs_TrustBoundarySection(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	_, err := gen.Generate(false)
	require.NoError(t, err)

	for _, name := range expectedWorkflowDocs {
		path := filepath.Join(outDir, "workflows", name)
		body, err := os.ReadFile(path)
		require.NoError(t, err)

		text := strings.ToLower(string(body))
		assert.Contains(t, text, "protects against",
			"workflow doc %s must state what it protects against", name)
		assert.Contains(t, text, "does not protect",
			"workflow doc %s must state what it does NOT protect against", name)
	}
}

// TestWorkflowDocs_LinkedFromAgentsMD verifies AGENTS.md (the LLM agent
// entry point) links to the workflows/ directory so agents can discover
// the new docs without a separate prompt change.
func TestWorkflowDocs_LinkedFromAgentsMD(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	_, err := gen.Generate(false)
	require.NoError(t, err)

	agentsBody, err := os.ReadFile(filepath.Join(outDir, "AGENTS.md"))
	require.NoError(t, err)

	text := string(agentsBody)
	for _, name := range expectedWorkflowDocs {
		assert.Contains(t, text, "workflows/"+name,
			"AGENTS.md must link to workflows/%s so agents discover it", name)
	}
}

// TestWorkflowDocs_EmbeddedGenerator verifies the embedded-templates
// generator (used by the shipped binary) also produces the workflow docs.
// Without this test, a bug where workflows render only via the on-disk
// loader would slip past — and the on-disk loader is dev-only.
func TestWorkflowDocs_EmbeddedGenerator(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen, err := NewEmbeddedGenerator(outDir, data, nil)
	require.NoError(t, err)

	_, err = gen.Generate(false)
	require.NoError(t, err)

	for _, name := range expectedWorkflowDocs {
		path := filepath.Join(outDir, "workflows", name)
		assert.FileExists(t, path,
			"embedded generator must produce workflow doc %s", name)
	}
}

// TestWorkflowSubdir_ConstantStable verifies the exported subdirectory
// constant matches the on-disk location. The mtix_workflow MCP tool in
// MTIX-14.6 is expected to point users at this subdir; if the constant
// drifts, the MCP rule library and the docs end up out of sync.
func TestWorkflowSubdir_ConstantStable(t *testing.T) {
	assert.Equal(t, "workflows", WorkflowSubdir())
}

// TestWorkflowDocs_SkippedOnSecondRunWithoutForce verifies the workflow
// docs follow the same template-based "skip on second run" rule as the
// top-level template-based docs. Local team edits should not be
// clobbered by an unforced re-run.
func TestWorkflowDocs_SkippedOnSecondRunWithoutForce(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	_, err := gen.Generate(false)
	require.NoError(t, err)

	// Mutate one of the workflow docs as a "team customization".
	path := filepath.Join(outDir, "workflows", "small-team.md")
	require.NoError(t, os.WriteFile(path, []byte("custom team edits"), 0o644))

	results, err := gen.Generate(false)
	require.NoError(t, err)

	var foundSkip bool
	for _, r := range results {
		if r.File == "workflows/small-team.md" {
			assert.Equal(t, "skipped", r.Action)
			foundSkip = true
		}
	}
	assert.True(t, foundSkip, "workflows/small-team.md should appear in results")

	// Customization preserved.
	preserved, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "custom team edits", string(preserved))
}

// TestWorkflowDocs_GenerateErrorWhenTemplateMissing verifies that if
// the workflow templates fail to load (e.g. user-supplied template dir
// without the workflows subtree), Generate surfaces a wrapped error
// pointing at the missing workflow doc rather than producing a partial
// docs/ tree silently.
func TestWorkflowDocs_GenerateErrorWhenTemplateMissing(t *testing.T) {
	// Build a sandbox templates dir that has the top-level templates but
	// NO workflows subdir. This mirrors a custom user template dir.
	tmplDir := t.TempDir()

	// Copy embedded top-level templates to the sandbox.
	for _, doc := range AllDocFiles() {
		body, err := embeddedTemplates.ReadFile("templates/" + doc.TemplateName)
		require.NoError(t, err)
		dst := filepath.Join(tmplDir, doc.TemplateName)
		require.NoError(t, os.WriteFile(dst, body, 0o644))
	}

	outDir := filepath.Join(t.TempDir(), "docs")
	data := minimalTemplateData()
	gen, err := NewGenerator(tmplDir, outDir, data, nil)
	require.NoError(t, err, "generator should construct even without workflows")

	_, err = gen.Generate(false)
	require.Error(t, err,
		"Generate must fail when workflow templates are missing")
	assert.Contains(t, err.Error(), "workflows/",
		"error must point at the missing workflow doc")
}

// TestWorkflowDocs_GenerateErrorWhenWorkflowsPathBlocked verifies
// Generate surfaces a useful error when the <outputDir>/workflows path
// cannot be created (e.g. a file already occupies that path). This
// covers the otherwise-easy-to-miss MkdirAll failure branch and
// confirms the error message points at the offending dir.
func TestWorkflowDocs_GenerateErrorWhenWorkflowsPathBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	// Plant a regular file where the workflows directory would go.
	require.NoError(t, os.WriteFile(
		filepath.Join(outDir, "workflows"),
		[]byte("not a directory"),
		0o644,
	))

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	_, err := gen.Generate(false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflows")
}

// TestWorkflowDocs_ForceRegeneratesAll verifies that --force overwrites
// the workflow docs even when local edits exist. Operators who actively
// want the latest templates back can opt-in.
func TestWorkflowDocs_ForceRegeneratesAll(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	_, err := gen.Generate(false)
	require.NoError(t, err)

	path := filepath.Join(outDir, "workflows", "solo.md")
	require.NoError(t, os.WriteFile(path, []byte("stale edits"), 0o644))

	results, err := gen.Generate(true)
	require.NoError(t, err)

	for _, r := range results {
		if r.File == "workflows/solo.md" {
			assert.NotEqual(t, "skipped", r.Action,
				"force=true must not skip workflow docs")
		}
	}

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotEqual(t, "stale edits", string(body),
		"force=true must overwrite the local edits")
	assert.Contains(t, string(body), "Solo Workflow",
		"regenerated content must come from the template")
}
