// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path"
	"strings"
)

const leaseSQLitePath = "github.com/hyper-swe/mtix/internal/store/sqlite"
const leaseServicePath = "github.com/hyper-swe/mtix/internal/service"

// This receiver-only check supplies imported type identities, not fake domain
// implementations. Unrelated imported APIs remain incomplete. Any unresolved
// lease receiver is rejected; compiler errors cannot make the guard pass.
// The compiler and full bar independently check the real imported APIs.
func leaseBoundaryViolations(fset *token.FileSet, files []*ast.File) []string {
	info := types.Info{Types: make(map[ast.Expr]types.TypeAndValue)}
	conf := types.Config{Importer: &leaseBoundaryImporter{packages: make(map[string]*types.Package)}, Error: func(error) {}}
	_, _ = conf.Check("lease-boundary", fset, files, &info)
	var violations []string
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if ok && leaseStorageMethod(sel.Sel.Name) && leaseStorageType(info.Types[sel.X].Type, make(map[types.Type]bool)) {
				violations = append(violations, fmt.Sprintf("%s: %s", fset.Position(sel.Pos()), sel.Sel.Name))
			}
			return true
		})
	}
	return violations
}

func leaseStorageMethod(name string) bool {
	switch name {
	case "AcquireWakeLease", "RenewWakeLease", "ReleaseWakeLease", "AbandonWakeLease":
		return true
	default:
		return false
	}
}

// Raw capability interfaces and embedded storage wrappers cannot be used to
// route around the service. Reflection and runtime closure contents are outside
// this static guard; unresolved selector receivers fail closed.
func leaseStorageType(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || t == types.Typ[types.Invalid] {
		return true
	}
	t = types.Unalias(t)
	if seen[t] {
		return false
	}
	seen[t] = true
	if ptr, ok := t.(*types.Pointer); ok {
		return leaseStorageType(ptr.Elem(), seen)
	}
	if named, ok := t.(*types.Named); ok {
		obj := named.Obj()
		if obj.Pkg() != nil && obj.Pkg().Path() == leaseSQLitePath && obj.Name() == "Store" {
			return true
		}
	}
	switch underlying := t.Underlying().(type) {
	case *types.Interface:
		return true
	case *types.Struct:
		for i := 0; i < underlying.NumFields(); i++ {
			field := underlying.Field(i)
			if field.Embedded() && leaseStorageType(field.Type(), seen) {
				return true
			}
		}
	}
	return false
}

type leaseBoundaryImporter struct{ packages map[string]*types.Package }

func (i *leaseBoundaryImporter) Import(importPath string) (*types.Package, error) {
	if pkg := i.packages[importPath]; pkg != nil {
		return pkg, nil
	}
	pkg := types.NewPackage(importPath, strings.Split(path.Base(importPath), ".")[0])
	i.packages[importPath] = pkg
	switch importPath {
	case leaseSQLitePath:
		st := leaseBoundaryNamed(pkg, "Store")
		leaseBoundaryConstructor(pkg, "New", st, true)
		leaseBoundaryConstructor(pkg, "NewSQLiteStore", st, true)
	case leaseServicePath:
		svc := leaseBoundaryNamed(pkg, "WakeLeaseService")
		leaseBoundaryConstructor(pkg, "NewWakeLeaseService", svc, false)
	}
	pkg.MarkComplete()
	return pkg, nil
}

func leaseBoundaryNamed(pkg *types.Package, name string) *types.Named {
	obj := types.NewTypeName(token.NoPos, pkg, name, nil)
	named := types.NewNamed(obj, types.NewStruct(nil, nil), nil)
	pkg.Scope().Insert(obj)
	return named
}

func leaseBoundaryConstructor(pkg *types.Package, name string, result *types.Named, withError bool) {
	anyType := types.NewInterfaceType(nil, nil).Complete()
	params := types.NewTuple(types.NewVar(token.NoPos, pkg, "args", types.NewSlice(anyType)))
	results := []*types.Var{types.NewVar(token.NoPos, pkg, "", types.NewPointer(result))}
	if withError {
		results = append(results, types.NewVar(token.NoPos, pkg, "", types.Universe.Lookup("error").Type()))
	}
	signature := types.NewSignatureType(nil, nil, nil, params, types.NewTuple(results...), true)
	pkg.Scope().Insert(types.NewFunc(token.NoPos, pkg, name, signature))
}
