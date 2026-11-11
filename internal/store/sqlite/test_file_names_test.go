// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reviewRoundRe matches a review-round marker in a test file or function
// name: an underscore followed by r and digits (_r2), or R and a digit right
// after Fields (FieldsR2). Tests are named after the behaviour they pin, not
// the review round that produced them.
var reviewRoundRe = regexp.MustCompile(`_r[0-9]+|FieldsR[0-9]`)

// TestTestFileNames_NoReviewRoundSuffix fails when a *_test.go file of this
// package or of cmd/mtix, or a Test function in one, carries a review-round
// marker in its name.
func TestTestFileNames_NoReviewRoundSuffix(t *testing.T) {
	for _, name := range []string{"replicated_fields_r2_test.go", "TestReplicatedFieldsR2_X", "TestSyncReplicatedFieldsR2_X"} {
		require.Regexp(t, reviewRoundRe, name, "the marker pattern must catch %s", name)
	}
	var bad []string
	for _, dir := range []string{".", filepath.Join("..", "..", "..", "cmd", "mtix")} {
		paths, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		require.NoError(t, err)
		require.NotEmpty(t, paths, "no test files found in %s", dir)
		for _, p := range paths {
			if reviewRoundRe.MatchString(filepath.Base(p)) {
				bad = append(bad, "file "+filepath.ToSlash(p))
			}
			src, err := os.ReadFile(p) //nolint:gosec // G304: a path globbed from this repository's own tree.
			require.NoError(t, err)
			file, err := parser.ParseFile(token.NewFileSet(), p, src, parser.SkipObjectResolution)
			require.NoError(t, err, p)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
					continue
				}
				if reviewRoundRe.MatchString(fn.Name.Name) {
					bad = append(bad, "func "+fn.Name.Name+" in "+filepath.ToSlash(p))
				}
			}
		}
	}
	assert.Empty(t, bad, "rename after the behaviour under test:\n%s", strings.Join(bad, "\n"))
}
