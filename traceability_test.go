// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Traceability gate per MTIX-26.8: every safety-critical scenario declared
// in QUALITY-STANDARDS.md §3.6 must map to at least one real test function
// in docs/traceability.json — a declared scenario without a linked,
// existing test fails this build. This is the control that prevents the
// declared-but-never-implemented drift found in the 2026-05-19 RCA
// (scenarios #5 and #10 had no tests for months and nothing noticed).
package mtix_test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// traceabilityEntry is one row of docs/traceability.json.
type traceabilityEntry struct {
	Scenario int      `json:"scenario"`
	Title    string   `json:"title"`
	Tests    []string `json:"tests"`
}

// scenarioHeadingRE matches the numbered scenario list in §3.6, e.g.
// `1. **State Machine Exhaustive Testing:** ...`.
var scenarioHeadingRE = regexp.MustCompile(`(?m)^(\d+)\. \*\*(.+?):?\*\*`)

// TestTraceability_EveryDeclaredScenarioHasExistingTests is the gate.
func TestTraceability_EveryDeclaredScenarioHasExistingTests(t *testing.T) {
	declared := declaredScenarios(t)
	require.NotEmpty(t, declared, "QUALITY-STANDARDS.md §3.6 scenario list not found")

	raw, err := os.ReadFile("docs/traceability.json")
	require.NoError(t, err,
		"docs/traceability.json is missing — every QUALITY-STANDARDS §3.6 scenario must be mapped to tests")
	var entries []traceabilityEntry
	require.NoError(t, json.Unmarshal(raw, &entries))

	mapped := map[int]traceabilityEntry{}
	for _, e := range entries {
		mapped[e.Scenario] = e
	}

	testIndex := indexTestFunctions(t)

	for num, title := range declared {
		entry, ok := mapped[num]
		if !assert.True(t, ok,
			"scenario %d (%s) is declared in QUALITY-STANDARDS §3.6 but has no traceability entry", num, title) {
			continue
		}
		assert.NotEmpty(t, entry.Tests,
			"scenario %d (%s) maps to zero tests — a declared scenario without a test is marketing, not engineering", num, title)
		for _, name := range entry.Tests {
			assert.Contains(t, testIndex, name,
				"scenario %d references test %q which does not exist in any *_test.go", num, name)
		}
	}
}

// declaredScenarios parses the §3.6 numbered list from QUALITY-STANDARDS.md.
func declaredScenarios(t *testing.T) map[int]string {
	t.Helper()
	raw, err := os.ReadFile("QUALITY-STANDARDS.md")
	require.NoError(t, err)

	// Constrain parsing to the §3.6 section.
	content := string(raw)
	start := strings.Index(content, "### 3.6")
	require.GreaterOrEqual(t, start, 0)
	rest := content[start:]
	if end := strings.Index(rest[1:], "### "); end >= 0 {
		rest = rest[:end+1]
	}

	out := map[int]string{}
	for _, m := range scenarioHeadingRE.FindAllStringSubmatch(rest, -1) {
		var n int
		_, scanErr := fmt.Sscanf(m[1], "%d", &n)
		require.NoError(t, scanErr)
		out[n] = m[2]
	}
	return out
}

// indexTestFunctions collects every `func TestXxx(` name in the repo.
func indexTestFunctions(t *testing.T) map[string]bool {
	t.Helper()
	re := regexp.MustCompile(`(?m)^func (Test\w+)\(`)
	index := map[string]bool{}

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "web" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			index[m[1]] = true
		}
		return nil
	})
	require.NoError(t, err)
	return index
}
