// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// unprofessionalWordingRE matches the two terms mtix output and shipped docs
// must not use (MTIX-95.11.3): guidance names the mechanism (push, pending 0,
// the typed ticket count at an interactive terminal) or escalates to a role.
var unprofessionalWordingRE = regexp.MustCompile(`(?i)human|go-ahead`)

// storedPromptMarker is exempt: stored prompt marker (FR-12.3a); renamed with
// compatibility in 0.6.0 (MTIX-107.95). Only this exact token is exempt; every
// other use of the terms still fails.
const storedPromptMarker = "[HUMAN-AUTHORED]"

// offendingTerm returns the first forbidden term in text outside the exempt
// stored prompt marker, or "".
func offendingTerm(text string) string {
	return unprofessionalWordingRE.FindString(strings.ReplaceAll(text, storedPromptMarker, ""))
}

// TestProfessionalWording_ExemptsOnlyTheStoredPromptMarker pins the exemption.
func TestProfessionalWording_ExemptsOnlyTheStoredPromptMarker(t *testing.T) {
	require.Empty(t, offendingTerm("each prompt is marked [HUMAN-AUTHORED] or [LLM-GENERATED]"))
	require.NotEmpty(t, offendingTerm("ask a human; the prompt is marked [HUMAN-AUTHORED]"))
	require.NotEmpty(t, offendingTerm("get the go-ahead"))
	require.NotEmpty(t, offendingTerm("[HUMAN-AUTHORED] human"))
}

// TestProfessionalWording_NoGoStringLiteralNamesHumanOrGoAhead fails if any
// non-test Go string literal under cmd/ and internal/ contains "human" or
// "go-ahead". Identifiers and comments are out of scope: only text a user,
// agent or API client can read at run time counts.
func TestProfessionalWording_NoGoStringLiteralNamesHumanOrGoAhead(t *testing.T) {
	root := filepath.Join("..", "..")
	var hits []string
	scanned := 0
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			scanned++
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					val = lit.Value
				}
				if m := offendingTerm(val); m != "" {
					hits = append(hits, fset.Position(lit.Pos()).String()+": "+m)
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	require.Greater(t, scanned, 100, "the scan found too few Go files; the walk is broken")
	require.Empty(t, hits, "Go string literals use unprofessional wording; state the mechanism or escalate to the project maintainer")
}

// TestProfessionalWording_NoShippedDocNamesHumanOrGoAhead fails if any shipped
// document contains "human" or "go-ahead": the generated templates, both
// plugin mirrors, README.md, USERMANUAL.md, docs/ and the generated CLI
// reference. Agent instructions escalate with "ask the user".
func TestProfessionalWording_NoShippedDocNamesHumanOrGoAhead(t *testing.T) {
	root := filepath.Join("..", "..")
	var hits []string
	scanned := 0
	scan := func(path string) {
		b, err := os.ReadFile(path) //nolint:gosec // test reads repo files under fixed roots
		require.NoError(t, err)
		scanned++
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(b), "\n") {
			if offendingTerm(line) != "" {
				hits = append(hits, filepath.ToSlash(rel)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	for _, dir := range []string{"internal/docs/templates", ".claude-plugin", ".codex-plugin", "docs"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				scan(path)
			}
			return nil
		})
		require.NoError(t, err)
	}
	scan(filepath.Join(root, "README.md"))
	scan(filepath.Join(root, "USERMANUAL.md"))
	require.Greater(t, scanned, 30, "the scan found too few documents; the walk is broken")
	require.Empty(t, hits, "shipped documents use unprofessional wording; ask the user, or name the mechanism")
}
