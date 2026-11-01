// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuarantineReceiptLookups_DocumentsTransportTradeoff(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "sync_quarantine_batch.go", nil, parser.ParseComments)
	require.NoError(t, err)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "quarantineReceiptLookups" {
			continue
		}
		require.NotNil(t, fn.Doc, "receipt lookup helper must document its transport tradeoff")
		// Check concepts, allowing ordinary rewordings rather than a prose snapshot.
		for _, concept := range []string{
			`(?i)\b(hex|base[- ]?16)\b`,
			`(?i)(\b(twice|doubl\w*|two)\b|2\s*[x×])`,
			`(?i)\bbytes?\b`,
			`(?i)\bjson\b`,
			`(?i)\b(fewer|less|reduc\w*|lower)\b`,
			`(?i)\b(round[\s-]*trips?|queries)\b`,
			`QuarantineAcknowledgementPageSize|\b500\b`,
		} {
			require.Regexp(t, concept, fn.Doc.Text(), "missing transport tradeoff concept")
		}
		return
	}
	t.Fatal("receipt lookup helper was not found")
}
