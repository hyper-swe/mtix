// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Portable AST guards inspect the actual native fixture helper. They establish
// source constraints, not Windows execution or native SID allocation safety.
package mtix_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStockFixtureSID_ActualHelper_ExcludesPointerReturningGetters(t *testing.T) {
	source, err := os.ReadFile("internal/oplocal/stock_acl_fixture_windows_test.go")
	require.NoError(t, err)
	require.NoError(t, stockFixtureSIDGetterError(source))
}

func TestStockFixtureSID_ActualHelper_AllocatesIndependentNativeAlias(t *testing.T) {
	source, err := os.ReadFile("internal/oplocal/stock_acl_fixture_windows_test.go")
	require.NoError(t, err)
	require.NoError(t, stockFixtureSIDAllocationError(source))
}

func stockFixtureSIDParse(source []byte) (*ast.File, *ast.FuncDecl, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, parser.AllErrors)
	if err != nil {
		return nil, nil, fmt.Errorf("parse fixture helper: %w", err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "stockFixtureLocalAdministrator" && function.Body != nil {
			return file, function, nil
		}
	}
	return nil, nil, fmt.Errorf("native local administrator helper missing")
}

func stockFixtureSIDGetterError(source []byte) error {
	file, _, err := stockFixtureSIDParse(source)
	if err != nil {
		return err
	}
	var forbidden string
	ast.Inspect(file, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok {
			switch identifier.Name {
			case "SubAuthorityCount", "SubAuthority", "IdentifierAuthority", "uintptr", "unsafe":
				forbidden = identifier.Name
			}
		}
		return true
	})
	for _, imported := range file.Imports {
		if imported.Path.Value == `"unsafe"` {
			forbidden = "unsafe import"
		}
	}
	if forbidden != "" {
		return fmt.Errorf("fixture helper uses forbidden pointer/getter path %s", forbidden)
	}
	return nil
}

func stockFixtureSIDAllocationError(source []byte) error {
	file, helper, err := stockFixtureSIDParse(source)
	if err != nil {
		return err
	}
	qualifier := stockFixtureSIDWindowsQualifier(file)
	if qualifier == "" {
		return fmt.Errorf("native windows API import missing")
	}
	aliases := map[string]string{}
	for _, statement := range helper.Body.List {
		assignment, ok := statement.(*ast.AssignStmt)
		if ok && len(assignment.Rhs) == 1 && len(assignment.Lhs) > 0 {
			stockFixtureSIDRecordAssignment(assignment, qualifier, aliases)
		}
		returned, ok := statement.(*ast.ReturnStmt)
		if ok && len(returned.Results) == 1 {
			name, ok := returned.Results[0].(*ast.Ident)
			if ok && aliases[name.Name] == "independent copy" {
				return nil
			}
		}
	}
	return fmt.Errorf("native O:LA owner string must feed StringToSid then Copy and return the independent copy")
}

func stockFixtureSIDWindowsQualifier(file *ast.File) string {
	for _, imported := range file.Imports {
		if imported.Path.Value != `"golang.org/x/sys/windows"` {
			continue
		}
		if imported.Name == nil {
			return "windows"
		}
		if imported.Name.Name != "." && imported.Name.Name != "_" {
			return imported.Name.Name
		}
	}
	return ""
}

func stockFixtureSIDRecordAssignment(assignment *ast.AssignStmt, qualifier string, aliases map[string]string) {
	name, ok := assignment.Lhs[0].(*ast.Ident)
	if !ok {
		return
	}
	// Every reassignment invalidates the earlier provenance, including decoys.
	delete(aliases, name.Name)
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	if !ok {
		return
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok {
		return
	}
	kind := stockFixtureSIDCallKind(call, selector.Sel.Name, receiver.Name, qualifier, aliases)
	if kind != "" {
		aliases[name.Name] = kind
	}
}

func stockFixtureSIDCallKind(call *ast.CallExpr, method, receiver, qualifier string, aliases map[string]string) string {
	if receiver == qualifier && method == "SecurityDescriptorFromString" && len(call.Args) == 1 {
		literal, ok := call.Args[0].(*ast.BasicLit)
		if ok && literal.Kind == token.STRING {
			text, err := strconv.Unquote(literal.Value)
			if err == nil && text == "O:LA" {
				return "native alias descriptor"
			}
		}
	}
	if receiver == qualifier && method == "StringToSid" && len(call.Args) == 1 {
		text, ok := call.Args[0].(*ast.Ident)
		if ok && aliases[text.Name] == "native owner string" {
			return "allocated native alias"
		}
	}
	if len(call.Args) != 0 {
		return ""
	}
	switch {
	case method == "Owner" && aliases[receiver] == "native alias descriptor":
		return "native owner"
	case method == "String" && aliases[receiver] == "native owner":
		return "native owner string"
	case method == "Copy" && aliases[receiver] == "allocated native alias":
		return "independent copy"
	}
	return ""
}

// SYNTHETIC source controls are parsed only; they never execute Windows APIs.
func syntheticStockFixtureSIDSource() string {
	return `package oplocal
import "golang.org/x/sys/windows"
func stockFixtureLocalAdministrator(t any) *windows.SID {
 alias, err := windows.SecurityDescriptorFromString("O:LA")
 owner, _, err := alias.Owner()
 text := owner.String()
 allocated, err := windows.StringToSid(text)
 principal, err := allocated.Copy()
 return principal
}`
}

func TestStockFixtureSID_SYNTHETICGetterControls_RejectUnsafePaths(t *testing.T) {
	valid := syntheticStockFixtureSIDSource()
	tests := []struct {
		name, source string
		invalid      bool
	}{
		{"valid API allocation", valid, false},
		{"comment decoy", valid + "\n// SubAuthorityCount unsafe uintptr", false},
		{"count getter", strings.Replace(valid, "text := owner.String()", "text := owner.SubAuthorityCount()", 1), true},
		{"subauthority getter", strings.Replace(valid, "text := owner.String()", "text := owner.SubAuthority(4)", 1), true},
		{"authority getter", strings.Replace(valid, "text := owner.String()", "text := owner.IdentifierAuthority()", 1), true},
		{"uintptr conversion", strings.Replace(valid, "text := owner.String()", "text := uintptr(1)", 1), true},
		{"byte SID cast", strings.Replace(valid, "text := owner.String()", "text := (*windows.SID)(unsafe.Pointer(&bytes[0]))", 1), true},
		{"aliased unsafe import", strings.Replace(valid, "import \"golang.org/x/sys/windows\"", "import (\"golang.org/x/sys/windows\"; u \"unsafe\")", 1), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := stockFixtureSIDGetterError([]byte(test.source))
			if test.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestStockFixtureSID_SYNTHETICAllocationControls_RejectBrokenProvenance(t *testing.T) {
	valid := syntheticStockFixtureSIDSource()
	tests := []struct {
		name, old, replacement string
		invalid                bool
	}{
		{"valid native alias", "", "", false},
		{"missing native LA", `"O:LA"`, `"O:BA"`, true},
		{"hardcoded SID", "text := owner.String()", `text := "S-1-5-21-1-2-3-500"`, true},
		{"missing StringToSid", "allocated, err := windows.StringToSid(text)", "allocated := owner", true},
		{"missing Copy", "principal, err := allocated.Copy()", "principal := allocated", true},
		{"owner copy only", "allocated.Copy()", "owner.Copy()", true},
		{"return owner", "return principal", "return owner", true},
		{"return allocation", "return principal", "return allocated", true},
		{"commented allocation", "allocated, err := windows.StringToSid(text)", "// allocated, err := windows.StringToSid(text)", true},
		{"overwritten copy", "return principal", "principal = owner\nreturn principal", true},
		{"wrong API qualifier", "windows.StringToSid(text)", "decoy.StringToSid(text)", true},
		{"wrong allocation input", "windows.StringToSid(text)", "windows.StringToSid(other)", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := valid
			if test.old != "" {
				source = strings.Replace(source, test.old, test.replacement, 1)
			}
			err := stockFixtureSIDAllocationError([]byte(source))
			if test.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
