// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// shippedDocSet is the set of files a customer's agent or operator reads:
// the generated templates, both plugin mirrors, the user manual and docs/.
// The auto-generated CLI reference is excluded: it is rendered from command
// help, which has its own tests.
type shippedDocSet struct {
	// agent holds the agent instruction set: templates and plugin mirrors.
	agent map[string]string
	// all holds agent plus USERMANUAL.md and docs/**/*.md.
	all map[string]string
}

// loadShippedDocs reads every shipped instruction file, keyed by its path
// relative to the repository root.
func loadShippedDocs(t *testing.T) shippedDocSet {
	t.Helper()
	root := filepath.Join("..", "..")
	set := shippedDocSet{agent: map[string]string{}, all: map[string]string{}}

	walk := func(dir string, into ...map[string]string) {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !isShippedDocFile(path) {
				return nil
			}
			b, rerr := os.ReadFile(path) //nolint:gosec // test reads repo files under fixed roots
			if rerr != nil {
				return rerr
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			for _, m := range into {
				m[filepath.ToSlash(rel)] = string(b)
			}
			return nil
		})
		require.NoError(t, err, "walk %s", dir)
	}
	for _, dir := range []string{"internal/docs/templates", ".claude-plugin", ".codex-plugin"} {
		walk(dir, set.agent, set.all)
	}
	walk("docs", set.all)

	um, err := os.ReadFile(filepath.Join(root, "USERMANUAL.md"))
	require.NoError(t, err)
	set.all["USERMANUAL.md"] = string(um)

	require.NotEmpty(t, set.agent, "no agent docs found: the scan is not reaching the templates")
	delete(set.all, "docs/CLI_REFERENCE.md")
	return set
}

// isShippedDocFile reports whether path is a text instruction file.
func isShippedDocFile(path string) bool {
	switch filepath.Ext(path) {
	case ".md", ".tmpl":
		return true
	}
	return false
}

var blankLineRE = regexp.MustCompile(`\n[ \t]*\n`)

// paragraphs splits text into blank-line-separated blocks, each paired with
// its 1-based first line. A paragraph is the unit the docs tests judge: a
// bullet, a table row, a wrapped sentence group or a code block each sit in
// one paragraph unless the author separates them with a blank line.
func paragraphs(text string) []paragraph {
	var out []paragraph
	line := 1
	for _, block := range splitKeepingLines(text) {
		out = append(out, paragraph{text: block.text, line: line + block.offset})
	}
	return out
}

type paragraph struct {
	text string
	line int
}

type rawBlock struct {
	text   string
	offset int
}

// splitKeepingLines splits on blank lines and records each block's starting
// line offset.
func splitKeepingLines(text string) []rawBlock {
	var blocks []rawBlock
	pos := 0
	for _, loc := range blankLineRE.FindAllStringIndex(text, -1) {
		blocks = append(blocks, rawBlock{text: text[pos:loc[0]], offset: strings.Count(text[:pos], "\n")})
		pos = loc[1]
	}
	blocks = append(blocks, rawBlock{text: text[pos:], offset: strings.Count(text[:pos], "\n")})
	return blocks
}

// itoa renders a line number for failure messages.
func itoa(n int) string { return strconv.Itoa(n) }
