// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// Layering guards catch accidental recurrence at the HTTP service boundary and
// mutation boundary of gRPC/MCP; their reads remain tracked by MTIX-127.
// Per MTIX-120 ruling3345, supported scope is direct backend calls, simple local
// or cross-file package aliases, and storage imports in any Go quoting form.
// Known deliberate-evasion limits include asserted/type-switch aliases,
// container/field projections, returned functions, aliases reassigned through
// conditionals/loops/closures, and reflection. Existing checks may detect some
// advanced forms, but comprehensive detection is not an acceptance requirement.
// These limits are documented S3 follow-up work, not blocking scope failures;
// see TestServiceLayer_GuardKnownDeliberateEvasionLimits. This does not relax
// the accidental recurrence checks or alter any production behavior.
func TestServiceLayer_HTTPHasNoStoreAndTransportsHaveNoDirectMutations(t *testing.T) {
	for _, dir := range []string{".", "../grpc", "../../mcp"} {
		paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
		require.NoError(t, err)
		sources := map[string]string{}
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			sources[path] = string(content)
			if dir == "." {
				require.Empty(t, httpBackendImports(string(content)), path)
			}
		}
		require.Empty(t, directPackageStoreMutations(sources), "direct backend mutation in %s", dir)
	}
}

type serviceLayerEvents struct{ events []service.Event }

func (r *serviceLayerEvents) Broadcast(_ context.Context, e service.Event) error {
	r.events = append(r.events, e)
	return nil
}

// Every previously direct workflow/dependency/annotation handler must invoke
// its service and broadcast only after persistence, per FR-7.5/FR-10.4.
func TestServiceLayer_MutationHandlersBroadcast(t *testing.T) {
	for _, tt := range []struct {
		name, path, method, body string
		event                    service.EventType
	}{
		{"claim", "/nodes/TEST-1/claim", "POST", `{}`, service.EventNodeClaimed},
		{"force claim", "/nodes/TEST-1/claim", "POST", `{"force":true}`, service.EventNodeClaimed},
		{"unclaim", "/nodes/TEST-1/unclaim", "POST", `{"reason":"release"}`, service.EventNodeUnclaimed},
		{"cancel", "/nodes/TEST-1/cancel", "POST", `{"reason":"cancel"}`, service.EventNodeCancelled},
		{"annotation", "/nodes/TEST-1/comment", "POST", `{"text":"hello"}`, service.EventNodeUpdated},
		{"dependency add", "/deps", "POST", `{"from_id":"TEST-1","to_id":"TEST-2","dep_type":"related"}`, service.EventDependencyAdded},
		{"dependency remove", "/deps/TEST-1/TEST-2?dep_type=related", "DELETE", ``, service.EventDependencyRemoved},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := testServer(t)
			bc := &serviceLayerEvents{}
			s.nodeSvc = service.NewNodeService(s.store, bc, &service.StaticConfig{}, s.logger, testClock())
			s.nodeWriter = s.nodeSvc.(NodeWriter)
			s.depWriter = service.NewDependencyService(s.store, bc, s.logger, testClock())
			createTestNode(t, s, "first", "TEST")
			createTestNode(t, s, "second", "TEST")
			if tt.name == "unclaim" {
				require.NoError(t, s.store.ClaimNode(context.Background(), "TEST-1", "writer"))
			}
			if tt.name == "force claim" {
				require.NoError(t, s.store.ClaimNode(context.Background(), "TEST-1", "old"))
				_, err := s.store.WriteDB().ExecContext(context.Background(), `UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`, "2000-01-01T00:00:00Z", "old")
				require.NoError(t, err)
			}
			if tt.name == "dependency remove" {
				require.NoError(t, s.store.AddDependency(context.Background(), &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeRelated, CreatedAt: testClock()()}))
			}
			bc.events = nil
			req := newLocalRequest(tt.method, "/api/v1"+tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "mtix")
			req.Header.Set("X-Agent-ID", "writer")
			out := httptest.NewRecorder()
			s.Router().ServeHTTP(out, req)
			require.Less(t, out.Code, 300, out.Body.String())
			require.Len(t, bc.events, 1, "owning service must publish committed mutation")
			require.Equal(t, tt.event, bc.events[0].Type)
			require.WithinDuration(t, testClock()(), bc.events[0].Timestamp, time.Second)
		})
	}
}

type serviceLayerReadSpy struct {
	store.Store
	calls map[string]int
}

func (r *serviceLayerReadSpy) GetActivity(ctx context.Context, id string, limit, offset int) ([]model.ActivityEntry, error) {
	r.calls["GetActivity"]++
	return r.Store.GetActivity(ctx, id, limit, offset)
}
func (r *serviceLayerReadSpy) GetDirectChildren(ctx context.Context, id string) ([]*model.Node, error) {
	r.calls["GetDirectChildren"]++
	return r.Store.GetDirectChildren(ctx, id)
}
func (r *serviceLayerReadSpy) GetAncestorChain(ctx context.Context, id string) ([]*model.Node, error) {
	r.calls["GetAncestorChain"]++
	return r.Store.GetAncestorChain(ctx, id)
}
func (r *serviceLayerReadSpy) GetSiblings(ctx context.Context, id string) ([]*model.Node, error) {
	r.calls["GetSiblings"]++
	return r.Store.GetSiblings(ctx, id)
}
func (r *serviceLayerReadSpy) DistinctProjects(ctx context.Context) ([]store.ProjectInfo, error) {
	r.calls["DistinctProjects"]++
	return r.Store.DistinctProjects(ctx)
}
func (r *serviceLayerReadSpy) ListNodes(ctx context.Context, f store.NodeFilter, o store.ListOptions) ([]*model.Node, int, error) {
	r.calls["ListNodes"]++
	return r.Store.ListNodes(ctx, f, o)
}
func TestServiceLayer_ReadHandlersInvokeNodeService(t *testing.T) {
	for _, tt := range []struct{ path, method string }{
		{"/nodes/TEST-1/activity", "GetActivity"}, {"/nodes/TEST-1/children", "GetDirectChildren"},
		{"/nodes/TEST-1/ancestors", "GetAncestorChain"}, {"/context/TEST-1", "GetSiblings"},
		{"/tree/TEST-1", "GetDirectChildren"}, {"/progress/TEST-1", "GetDirectChildren"},
		{"/search?q=first", "ListNodes"}, {"/blocked", "ListNodes"}, {"/orphans", "ListNodes"},
		{"/stats", "ListNodes"}, {"/projects", "DistinctProjects"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			s := testServer(t)
			createTestNode(t, s, "first", "TEST")
			spy := &serviceLayerReadSpy{Store: s.store, calls: map[string]int{}}
			s.nodeSvc = service.NewNodeService(spy, nil, &service.StaticConfig{}, s.logger, testClock())
			req := newLocalRequest("GET", "/api/v1"+tt.path, nil)
			out := httptest.NewRecorder()
			s.Router().ServeHTTP(out, req)
			require.Equal(t, 200, out.Code, out.Body.String())
			require.Greater(t, spy.calls[tt.method], 0, "handler must query through its service")
		})
	}
}

// This fake has no mutation methods, proving the read-only constructor needs
// read capabilities alone rather than a concrete writer filtered at runtime.
type onlyNodeReader struct{ NodeReader }

func (onlyNodeReader) GetNode(_ context.Context, id string) (*model.Node, error) {
	return &model.Node{ID: id, Title: "read only"}, nil
}
func TestServiceLayer_ReadOnlyConstructorServesReadsAndOmitsMutations(t *testing.T) {
	s := NewReadOnlyServer(ReadServices{Nodes: onlyNodeReader{}}, nil, ServerConfig{}, testClock())
	defer s.wsHub.Close()
	out := httptest.NewRecorder()
	s.Router().ServeHTTP(out, newLocalRequest("GET", "/api/v1/nodes/TEST-1", nil))
	require.Equal(t, 200, out.Code)
	require.Contains(t, out.Body.String(), "read only")
	for _, route := range s.Router().Routes() {
		require.Equal(t, "GET", route.Method, "read-only API cannot register mutation routes")
	}
}

type serviceLayerAdmin struct {
	verify, backup, close int
	fail                  error
}

func (a *serviceLayerAdmin) Verify(context.Context) (*service.IntegrityReport, error) {
	a.verify++
	return &service.IntegrityReport{Status: "ok"}, a.fail
}
func (a *serviceLayerAdmin) Backup(_ context.Context, path string) (*service.BackupResult, error) {
	a.backup++
	return &service.BackupResult{Path: path, Size: 123, Verified: true}, a.fail
}
func (a *serviceLayerAdmin) Close() error { a.close++; return a.fail }
func TestServiceLayer_AdminHandlersAndLifecycleInvokeService(t *testing.T) {
	for _, operation := range []string{"verify", "backup", "close"} {
		t.Run(operation, func(t *testing.T) {
			s := testServer(t)
			admin := &serviceLayerAdmin{}
			s.adminSvc = admin
			s.adminWriter = admin
			if operation == "close" {
				require.NoError(t, s.Shutdown(context.Background()))
				require.Equal(t, 1, admin.close)
				return
			}
			body := `{}`
			if operation == "backup" {
				body = `{"path":"` + filepath.Join(t.TempDir(), "backup.db") + `"}`
			}
			req := newLocalRequest("POST", "/api/v1/admin/"+operation, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "mtix")
			out := httptest.NewRecorder()
			s.Router().ServeHTTP(out, req)
			require.Equal(t, 200, out.Code, out.Body.String())
			if operation == "verify" {
				require.Equal(t, 1, admin.verify)
			} else {
				require.Equal(t, 1, admin.backup)
				require.Contains(t, out.Body.String(), "123")
			}
		})
	}
}

type serviceLayerDependencies struct{ calls int }

func (d *serviceLayerDependencies) GetBlockers(context.Context, string) ([]*model.Dependency, error) {
	d.calls++
	return []*model.Dependency{{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks}}, nil
}
func TestServiceLayer_DependencyQueryInvokesService(t *testing.T) {
	s := testServer(t)
	createTestNode(t, s, "first", "TEST")
	deps := &serviceLayerDependencies{}
	s.depSvc = deps
	out := httptest.NewRecorder()
	s.Router().ServeHTTP(out, newLocalRequest("GET", "/api/v1/deps/TEST-1", nil))
	require.Equal(t, 200, out.Code)
	require.Equal(t, 1, deps.calls)
	require.Contains(t, out.Body.String(), "TEST-2")
}

// packageBackendGuard resolves receiver types across all files in a package.
// Local binding scopes prevent shadowed service variables inheriting backend state.
type backendGuardBinding struct {
	backend       bool
	owner, method string
}
type backendGuardScope struct {
	parent       *backendGuardScope
	names        map[string]backendGuardBinding
	conservative bool
	types        map[string]backendGuardType
}

func (s *backendGuardScope) lookup(name string) backendGuardBinding {
	for scope := s; scope != nil; scope = scope.parent {
		if value, ok := scope.names[name]; ok {
			return value
		}
	}
	return backendGuardBinding{}
}
func (s *backendGuardScope) localType(name string) (backendGuardType, bool) {
	for scope := s; scope != nil; scope = scope.parent {
		if declaration, ok := scope.types[name]; ok {
			return declaration, true
		}
	}
	return backendGuardType{}, false
}
func (s *backendGuardScope) contains(name string) bool {
	for scope := s; scope != nil; scope = scope.parent {
		if _, ok := scope.names[name]; ok {
			return true
		}
	}
	return false
}
func (s *backendGuardScope) assign(name string, value backendGuardBinding, define bool) {
	if !define {
		for scope := s; scope != nil; scope = scope.parent {
			if previous, ok := scope.names[name]; ok {
				if scope.conservative {
					value = possibleBackendBinding(previous, value)
				}
				scope.names[name] = value
				return
			}
		}
	}
	s.names[name] = value
}

// A possible backend path is retained without evaluating branch conditions.
func possibleBackendBinding(previous, alternative backendGuardBinding) backendGuardBinding {
	if alternative.backend {
		previous.backend = true
	}
	if previous.owner == "" {
		previous.owner = alternative.owner
	}
	if alternative.method != "" && (previous.method == "" || backendReadMethod(previous.method) && !backendReadMethod(alternative.method)) {
		previous.method = alternative.method
	}
	return previous
}
func forkBackendScope(scope *backendGuardScope) *backendGuardScope {
	if scope == nil {
		return nil
	}
	fork := &backendGuardScope{parent: forkBackendScope(scope.parent), names: map[string]backendGuardBinding{}, conservative: true}
	for name, value := range scope.names {
		fork.names[name] = value
	}
	fork.types = map[string]backendGuardType{}
	for name, declaration := range scope.types {
		fork.types[name] = declaration
	}
	return fork
}
func joinBackendScopes(original, alternative *backendGuardScope) {
	for original != nil && alternative != nil {
		for name, previous := range original.names {
			original.names[name] = possibleBackendBinding(previous, alternative.names[name])
		}
		original, alternative = original.parent, alternative.parent
	}
}

type backendGuardFile struct {
	path    string
	tree    *ast.File
	imports map[string]bool
}
type backendGuardType struct {
	expr ast.Expr
	file *backendGuardFile
}
type packageBackendGuard struct {
	files    []*backendGuardFile
	types    map[string]backendGuardType
	globals  *backendGuardScope
	findings []string
}

func backendImport(path string) bool {
	return path == "github.com/hyper-swe/mtix/internal/store" || strings.HasPrefix(path, "github.com/hyper-swe/mtix/internal/store/")
}
func parseBackendGuardFile(path, source string) (*backendGuardFile, error) {
	tree, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		return nil, err
	}
	file := &backendGuardFile{path: path, tree: tree, imports: map[string]bool{}}
	for _, imp := range tree.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		if backendImport(path) {
			name := filepath.Base(path)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			file.imports[name] = true
		}
	}
	return file, nil
}
func httpBackendImports(source string) []string {
	file, err := parseBackendGuardFile("http.go", source)
	if err != nil {
		return []string{err.Error()}
	}
	var findings []string
	for _, imp := range file.tree.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			findings = append(findings, err.Error())
			continue
		}
		if backendImport(path) {
			findings = append(findings, path)
		}
	}
	return findings
}
func directStoreMutations(path, source string) []string {
	return directPackageStoreMutations(map[string]string{path: source})
}
func directPackageStoreMutations(sources map[string]string) []string {
	guard := &packageBackendGuard{types: map[string]backendGuardType{}, globals: &backendGuardScope{names: map[string]backendGuardBinding{}}}
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		file, err := parseBackendGuardFile(path, sources[path])
		if err != nil {
			return []string{err.Error()}
		}
		guard.files = append(guard.files, file)
		for _, declaration := range file.tree.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range group.Specs {
				if typed, ok := spec.(*ast.TypeSpec); ok {
					guard.types[typed.Name.Name] = backendGuardType{typed.Type, file}
				}
			}
		}
	}
	guard.inspectPackage()
	return guard.findings
}
func (g *packageBackendGuard) typeBinding(expr ast.Expr, file *backendGuardFile, seen map[string]bool) backendGuardBinding {
	switch typed := expr.(type) {
	case *ast.StarExpr:
		return g.typeBinding(typed.X, file, seen)
	case *ast.ParenExpr:
		return g.typeBinding(typed.X, file, seen)
	case *ast.SelectorExpr:
		name, ok := typed.X.(*ast.Ident)
		return backendGuardBinding{backend: ok && file.imports[name.Name]}
	case *ast.Ident:
		if typed.Name == "Store" && file.imports["."] {
			return backendGuardBinding{backend: true}
		}
		// Existing MCP persistence contract, distinct from InboxAcknowledger service.
		if typed.Name == "InboxStore" {
			return backendGuardBinding{backend: true}
		}
		declaration, ok := g.types[typed.Name]
		if !ok || seen[typed.Name] {
			return backendGuardBinding{owner: typed.Name}
		}
		switch declaration.expr.(type) {
		case *ast.StructType, *ast.InterfaceType:
			return backendGuardBinding{owner: typed.Name}
		}
		seen[typed.Name] = true
		return g.typeBinding(declaration.expr, declaration.file, seen)
	}
	return backendGuardBinding{}
}
func (g *packageBackendGuard) fieldBinding(owner, name string) (backendGuardBinding, bool) {
	declaration, ok := g.types[owner]
	if !ok {
		return backendGuardBinding{}, false
	}
	structure, ok := declaration.expr.(*ast.StructType)
	if !ok {
		return backendGuardBinding{}, false
	}
	for _, field := range structure.Fields.List {
		for _, id := range field.Names {
			if id.Name == name {
				return g.typeBinding(field.Type, declaration.file, map[string]bool{}), true
			}
		}
	}
	return backendGuardBinding{}, false
}
func (g *packageBackendGuard) expression(expr ast.Expr, scope *backendGuardScope, file *backendGuardFile) backendGuardBinding {
	switch value := expr.(type) {
	case *ast.Ident:
		return scope.lookup(value.Name)
	case *ast.ParenExpr:
		return g.expression(value.X, scope, file)
	case *ast.UnaryExpr:
		return g.expression(value.X, scope, file)
	case *ast.SelectorExpr:
		receiver := g.expression(value.X, scope, file)
		if g.receiverBackendType(value.X, scope, file) {
			receiver.backend = true
		}
		if field, ok := g.fieldBinding(receiver.owner, value.Sel.Name); ok {
			return field
		}
		if receiver.backend {
			return backendGuardBinding{method: value.Sel.Name}
		}
	case *ast.CallExpr:
		method := g.expression(value.Fun, scope, file).method
		if method == "ReadDB" || method == "WriteDB" {
			return backendGuardBinding{backend: true}
		}
		if g.receiverBackendType(value.Fun, scope, file) {
			return backendGuardBinding{backend: true}
		}
		if converted := g.backendConversion(value, scope, file); converted.backend || converted.method != "" {
			return converted
		}
	}
	return backendGuardBinding{}
}

// A type expression is distinct from a value that shadows its name/import.
func (g *packageBackendGuard) receiverBackendType(expr ast.Expr, scope *backendGuardScope, file *backendGuardFile) bool {
	switch value := expr.(type) {
	case *ast.ParenExpr:
		return g.receiverBackendType(value.X, scope, file)
	case *ast.StarExpr:
		return g.receiverBackendType(value.X, scope, file)
	case *ast.Ident:
		if scope.contains(value.Name) {
			return false
		}
		if declaration, local := scope.localType(value.Name); local {
			return g.receiverBackendType(declaration.expr, scope, declaration.file)
		}
	case *ast.SelectorExpr:
		if qualifier, ok := value.X.(*ast.Ident); !ok || scope.contains(qualifier.Name) {
			return false
		}
	default:
		return false
	}
	return g.typeBinding(expr, file, map[string]bool{}).backend
}

// Conversion to a local interface does not erase the original backend source.
func (g *packageBackendGuard) backendConversion(call *ast.CallExpr, scope *backendGuardScope, file *backendGuardFile) backendGuardBinding {
	if len(call.Args) != 1 {
		return backendGuardBinding{}
	}
	switch callee := call.Fun.(type) {
	case *ast.Ident:
		_, typed := g.types[callee.Name]
		_, local := scope.localType(callee.Name)
		if !typed && !local || scope.contains(callee.Name) {
			return backendGuardBinding{}
		}
	case *ast.InterfaceType:
	default:
		return backendGuardBinding{}
	}
	return g.expression(call.Args[0], scope, file)
}
func (g *packageBackendGuard) bindFields(fields *ast.FieldList, scope *backendGuardScope, file *backendGuardFile) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		value := g.typeBinding(field.Type, file, map[string]bool{})
		for _, name := range field.Names {
			scope.names[name.Name] = value
		}
	}
}
func (g *packageBackendGuard) bindValues(value *ast.ValueSpec, scope *backendGuardScope, file *backendGuardFile) {
	values := make([]backendGuardBinding, len(value.Names))
	for i := range values {
		values[i] = g.typeBinding(value.Type, file, map[string]bool{})
		if i < len(value.Values) {
			assigned := g.expression(value.Values[i], scope, file)
			if !values[i].backend && (assigned.backend || assigned.method != "" || value.Type == nil) {
				values[i] = assigned
			}
		}
	}
	for i, name := range value.Names {
		scope.assign(name.Name, values[i], true)
	}
}
func (g *packageBackendGuard) bindAssignment(value *ast.AssignStmt, scope *backendGuardScope, file *backendGuardFile) {
	values := make([]backendGuardBinding, len(value.Lhs))
	for i := range values {
		if i < len(value.Rhs) {
			values[i] = g.expression(value.Rhs[i], scope, file)
		}
	}
	for i, left := range value.Lhs {
		if name, ok := left.(*ast.Ident); ok {
			scope.assign(name.Name, values[i], value.Tok == token.DEFINE)
		}
	}
}
func (g *packageBackendGuard) inspectPackage() {
	// Global var aliases are resolved before function-local scopes.
	for round := 0; round <= len(g.globals.names)+len(g.files); round++ {
		for _, file := range g.files {
			for _, declaration := range file.tree.Decls {
				group, ok := declaration.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range group.Specs {
					if value, ok := spec.(*ast.ValueSpec); ok {
						g.bindValues(value, g.globals, file)
					}
				}
			}
		}
	}
	for _, file := range g.files {
		for _, declaration := range file.tree.Decls {
			if global, ok := declaration.(*ast.GenDecl); ok && global.Tok == token.VAR {
				g.walk(global, g.globals, file)
			}
			if function, ok := declaration.(*ast.FuncDecl); ok {
				scope := &backendGuardScope{parent: g.globals, names: map[string]backendGuardBinding{}}
				g.bindFields(function.Recv, scope, file)
				g.bindFields(function.Type.Params, scope, file)
				g.bindFields(function.Type.Results, scope, file)
				if function.Body != nil {
					g.walk(function.Body, scope, file)
				}
			}
		}
	}
}
func (g *packageBackendGuard) walk(node ast.Node, scope *backendGuardScope, file *backendGuardFile) {
	if node == nil {
		return
	}
	ast.Inspect(node, func(node ast.Node) bool {
		if g.controlScope(node, scope, file) {
			return false
		}
		g.rejectBackendReference(node, scope, file)
		switch value := node.(type) {
		case *ast.TypeSpec:
			if scope.types == nil {
				scope.types = map[string]backendGuardType{}
			}
			scope.types[value.Name.Name] = backendGuardType{value.Type, file}
		case *ast.BlockStmt:
			child := &backendGuardScope{parent: scope, names: map[string]backendGuardBinding{}}
			for _, statement := range value.List {
				g.walk(statement, child, file)
			}
			return false
		case *ast.FuncLit:
			fork := forkBackendScope(scope)
			child := &backendGuardScope{parent: fork, names: map[string]backendGuardBinding{}, conservative: true}
			g.bindFields(value.Type.Params, child, file)
			g.bindFields(value.Type.Results, child, file)
			g.walk(value.Body, child, file)
			joinBackendScopes(scope, fork)
			return false
		case *ast.AssignStmt:
			for _, expr := range value.Rhs {
				g.walk(expr, scope, file)
			}
			g.bindAssignment(value, scope, file)
			return false
		case *ast.ValueSpec:
			for _, expr := range value.Values {
				g.walk(expr, scope, file)
			}
			g.bindValues(value, scope, file)
			return false

		}
		return true
	})
}

// Reject the mutation selector itself, including a method value that is never
// invoked. Transport code must never obtain a backend write capability.
func (g *packageBackendGuard) rejectBackendReference(node ast.Node, scope *backendGuardScope, file *backendGuardFile) {
	var expression ast.Expr
	switch value := node.(type) {
	case *ast.SelectorExpr:
		expression = value
	case *ast.CallExpr:
		expression = value.Fun
	default:
		return
	}
	method := g.expression(expression, scope, file).method
	if method != "" && !backendReadMethod(method) {
		g.findings = append(g.findings, file.path+":"+method)
	}
}
func backendControlNode(node ast.Node) bool {
	switch node.(type) {
	case *ast.IfStmt, *ast.ForStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.RangeStmt, *ast.CaseClause, *ast.CommClause:
		return true
	}
	return false
}

// Control initializers have lexical scopes independent of the enclosing block.
func (g *packageBackendGuard) controlScope(node ast.Node, scope *backendGuardScope, file *backendGuardFile) bool {
	if !backendControlNode(node) {
		return false
	}
	fork := forkBackendScope(scope)
	defer joinBackendScopes(scope, fork)
	child := &backendGuardScope{parent: fork, names: map[string]backendGuardBinding{}, conservative: true}
	switch value := node.(type) {
	case *ast.IfStmt:
		g.walk(value.Init, child, file)
		g.walk(value.Cond, child, file)
		g.walk(value.Body, child, file)
		g.walk(value.Else, child, file)
	case *ast.ForStmt:
		g.walk(value.Init, child, file)
		g.walk(value.Cond, child, file)
		g.walk(value.Body, child, file)
		g.walk(value.Post, child, file)
	case *ast.SwitchStmt:
		g.walk(value.Init, child, file)
		g.walk(value.Tag, child, file)
		g.walk(value.Body, child, file)
	case *ast.TypeSwitchStmt:
		g.walk(value.Init, child, file)
		g.walk(value.Assign, child, file)
		g.walk(value.Body, child, file)
	case *ast.RangeStmt:
		g.walk(value.X, child, file)
		for _, expr := range []ast.Expr{value.Key, value.Value} {
			if name, ok := expr.(*ast.Ident); ok {
				child.assign(name.Name, backendGuardBinding{}, value.Tok == token.DEFINE)
			}
		}
		g.walk(value.Body, child, file)
	case *ast.CaseClause:
		for _, expr := range value.List {
			g.walk(expr, child, file)
		}
		for _, statement := range value.Body {
			g.walk(statement, child, file)
		}
	case *ast.CommClause:
		g.walk(value.Comm, child, file)
		for _, statement := range value.Body {
			g.walk(statement, child, file)
		}
	default:
		return false
	}
	return true
}

// Explicit reads preserve the gRPC/MCP query scope delegated to MTIX-127.
// Unknown future backend methods fail closed rather than evading review.
func backendReadMethod(name string) bool {
	switch name {
	case "GetNode", "ListNodes", "SearchNodes", "GetBlockers", "GetDirectChildren", "GetAncestorChain", "GetSiblings", "GetActivity", "DistinctProjects", "InboxList", "InboxWait", "ResolveUIDByDisplayPath", "ResolveDisplayPathByUID", "ReadDB", "Query", "QueryContext", "QueryRow", "QueryRowContext":
		return true
	}
	return false
}

func TestServiceLayer_MutationGuardRecognizesAliasedBackend(t *testing.T) {
	for _, source := range []string{
		`package sample;import backend "github.com/hyper-swe/mtix/internal/store";func f(db backend.Store){renamed:=db;renamed.CancelNode(nil,"","","",false)}`,
		`package sample;import db "github.com/hyper-swe/mtix/internal/store/sqlite";type server struct {storage *db.Store};func(s *server)f(){renamed:=s.storage;renamed.Backup(nil,"")}`,
	} {
		require.NotEmpty(t, directStoreMutations("mutation.go", source))
	}
	require.Empty(t, directStoreMutations("service.go", `package sample;func f(svc NodeWriter){svc.CancelNode(nil,"","","",false)}`))
}

// The measured HTTP coverage must satisfy the exact historical value and the
// coordinator's literal91.53 floor. Run with MTIX_HTTP_COVER_PROFILE after
// producing the same package coverprofile used for the baseline comparison.
func TestServiceLayer_HTTPMeasuredCoverageFloor(t *testing.T) {
	path := os.Getenv("MTIX_HTTP_COVER_PROFILE")
	if path == "" {
		t.Skip("coverage artifact is checked after the focused coverage run")
	}
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	total, covered := 0, 0
	for _, line := range strings.Split(string(content), "\n")[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var location string
		var statements, count int
		_, err := fmt.Sscan(line, &location, &statements, &count)
		require.NoError(t, err)
		total += statements
		if count > 0 {
			covered += statements
		}
	}
	require.Greater(t, total, 0)
	require.GreaterOrEqual(t, float64(covered)/float64(total), 0.9153)
	require.GreaterOrEqual(t, float64(covered)/float64(total), float64(778)/float64(850))
}

// Counterexamples reproduce the original cross-file receiver and common aliases.
func TestServiceLayer_GuardPackageBindingsAndAliases(t *testing.T) {
	server := "package sample; import backend \"github.com/hyper-swe/mtix/internal/store\"; type Server struct { store backend.Store; svc NodeWriter }"
	for _, call := range []string{
		`s.store.CancelNode(nil,"","","",false)`,
		`var db = s.store; db.CancelNode(nil,"","","",false)`,
		`var db backend.Store = nil; db.CancelNode(nil,"","","",false)`,
		`db := s.store; if db := s.svc; true { db.CancelNode(nil,"","","",false) }; db.CancelNode(nil,"","","",false)`,
		`var cancel = s.store.CancelNode; cancel(nil,"","","",false)`,
		`db := s.store; other := db; cancel := other.CancelNode; cancel(nil,"","","",false)`,
		`s.store.FutureBackendMutation()`,
		`s.store.ReadDB().ExecContext(nil, "")`,
		`var cancel func(...any)error; cancel = s.store.CancelNode; alias := cancel; alias(nil,"","","",false)`,
	} {
		sources := map[string]string{"server.go": server, "handlers.go": `package sample; import backend "github.com/hyper-swe/mtix/internal/store"; func(s *Server)HandleCancel(){` + call + "}"}
		require.NotEmpty(t, directPackageStoreMutations(sources), call)
	}
}
func TestServiceLayer_HTTPImportGuardHandlesAllGoLiterals(t *testing.T) {
	for _, literal := range []string{`"github.com/hyper-swe/mtix/internal/store"`, "`github.com/hyper-swe/mtix/internal/store/sqlite`", `"github.com/hyper-swe/mtix/internal/\x73tore/sqlite"`} {
		source := "package sample; import backend " + literal + "; type Server struct { store *backend.Store }"
		require.NotEmpty(t, httpBackendImports(source), literal)
	}
	require.Empty(t, httpBackendImports(`package sample; import "github.com/hyper-swe/mtix/internal/service"`))
}
func TestServiceLayer_GuardAllowsScopedServicesAndBackendReads(t *testing.T) {
	server := `package sample; import backend "github.com/hyper-swe/mtix/internal/store"; type Server struct { store backend.Store; svc NodeWriter }; type ServiceHolder struct { store NodeWriter }`
	for _, body := range []string{
		`s.store.GetNode(nil,""); var read = s.store.ListNodes; read(nil, nil, nil)`,
		`s.store.ReadDB().QueryRowContext(nil,"")`,
		`db := s.store; fn := func(db NodeWriter){db.CancelNode(nil,"","","",false)}; fn(s.svc); db.GetNode(nil,"")`,
		`var writer = s.svc; var cancel = writer.CancelNode; cancel(nil,"","","",false)`,
		`db := s.store; { db := s.svc; db.CancelNode(nil,"","","",false) }; db.GetNode(nil,"")`,
		`db := s.store; if db := s.svc; true { db.CancelNode(nil,"","","",false) }; db.GetNode(nil,"")`,
		`db := s.store; for db := s.svc; false; { db.CancelNode(nil,"","","",false) }; db.GetNode(nil,"")`,
	} {
		sources := map[string]string{"server.go": server, "handlers.go": "package sample; func(s *Server)f(){" + body + "}; func(h *ServiceHolder)g(){h.store.CancelNode(nil,\"\",\"\",\"\",false)}"}
		require.Empty(t, directPackageStoreMutations(sources), body)
	}
}

// Taking a backend mutation method value already crosses the service boundary;
// dead branches, deferred/uncalled closures, and alias overwrites cannot hide it.
func TestServiceLayer_GuardRejectsCapturedBackendMutationReferences(t *testing.T) {
	for _, overwrite := range []string{
		`if false { cancel = s.svc.CancelNode }`,
		`for false { cancel = s.svc.CancelNode }`,
		`unused := func(){ cancel = s.svc.CancelNode }; _ = unused`,
		`if true { cancel = s.svc.CancelNode } else { cancel = s.svc.CancelNode }`,
		`defer func(){ cancel = s.svc.CancelNode }()`,
		`cancel = s.svc.CancelNode`,
	} {
		t.Run(overwrite, func(t *testing.T) {
			source := `package sample; import backend "github.com/hyper-swe/mtix/internal/store"; type Server struct { store backend.Store; svc NodeWriter }; func(s *Server)f(){ var cancel func(...any)error = (s.store.CancelNode); ` + overwrite + `; (cancel)(nil,"","","",false) }`
			require.NotEmpty(t, directStoreMutations("guard.go", source))
		})
	}
}
func TestServiceLayer_GuardConservativelyJoinsPossibleBackendValues(t *testing.T) {
	for _, source := range []string{
		`var db interface{CancelNode(...any)error} = s.store; if false { db = s.svc }; db.CancelNode(nil)`,
		`var db interface{CancelNode(...any)error} = s.svc; if true { db = s.store } else { db = s.svc }; db.CancelNode(nil)`,
		`var db interface{CancelNode(...any)error} = s.store; for false { db = s.svc }; db.CancelNode(nil)`,
		`var db interface{CancelNode(...any)error} = s.store; unused := func(){db = s.svc}; _ = unused; db.CancelNode(nil)`,
	} {
		t.Run(source, func(t *testing.T) {
			declarations := `package sample; import backend "github.com/hyper-swe/mtix/internal/store"; type Server struct { store backend.Store; svc NodeWriter }; func(s *Server)f(){`
			require.NotEmpty(t, directStoreMutations("guard.go", declarations+source+`}`))
		})
	}
}
func TestServiceLayer_GuardCaptureControlsAllowReadsAndServices(t *testing.T) {
	for _, body := range []string{
		`read := (s.store.GetNode); if false {read = s.svc.GetNode}; (read)(nil,"")`,
		`cancel := (s.svc.CancelNode); if false {cancel = s.svc.CancelNode}; (cancel)(nil,"","","",false)`,
		`cancel := s.svc.CancelNode; for false {cancel = s.svc.CancelNode}; cancel(nil,"","","",false)`,
		`cancel := s.svc.CancelNode; unused:=func(){cancel=s.svc.CancelNode};_=unused;cancel(nil,"","","",false)`,
		`db:=s.store; if true {db:=s.svc; db.CancelNode(nil,"","","",false)}; db.GetNode(nil,"")`,
		`db:=s.store; unused:=func(db NodeWriter){db.CancelNode(nil,"","","",false)};_=unused;db.GetNode(nil,"")`,
	} {
		source := `package sample; import backend "github.com/hyper-swe/mtix/internal/store"; type Server struct { store backend.Store; svc NodeWriter }; func(s *Server)f(){` + body + `}`
		require.Empty(t, directStoreMutations("guard.go", source), body)
	}
}

func TestServiceLayer_GuardTracksBackendMethodExpressionsAndConversions(t *testing.T) {
	for _, body := range []string{
		`cancel:=backend.Store.CancelNode; cancel(s.store,nil,"","","",false)`,
		`cancel:=(*sqlbackend.Store).CancelNode; cancel(s.store,nil,"","","",false)`,
		`cancel:=Storage.CancelNode; cancel(s.store,nil,"","","",false)`,
		`type LocalStorage=backend.Store; cancel:=LocalStorage.CancelNode; cancel(s.store,nil,"","","",false)`,
		`type LocalAPI interface{CancelNode(...any)error}; db:=LocalAPI(s.store); db.CancelNode(nil)`,
		`db:=CancelAPI(s.store); db.CancelNode(nil,"","","",false)`,
	} {
		source := `package sample;import backend "github.com/hyper-swe/mtix/internal/store";import sqlbackend "github.com/hyper-swe/mtix/internal/store/sqlite";type Storage=backend.Store;type CancelAPI interface{CancelNode(...any)error};type Server struct{store backend.Store;svc NodeWriter};func(s *Server)f(){` + body + `}`
		require.NotEmpty(t, directStoreMutations("guard.go", source), body)
	}
	for _, body := range []string{
		`read:=backend.Store.GetNode; read(s.store,nil,"")`,
		`db:=CancelAPI(s.svc); db.CancelNode(nil,"","","",false)`,
	} {
		source := `package sample;import backend "github.com/hyper-swe/mtix/internal/store";type CancelAPI interface{CancelNode(...any)error};type Server struct{store backend.Store;svc NodeWriter};func(s *Server)f(){` + body + `}`
		require.Empty(t, directStoreMutations("guard.go", source), body)
	}
}

func TestServiceLayer_GuardAllowsValuesShadowingTypeNames(t *testing.T) {
	for _, source := range []string{
		`package sample; import backend "github.com/hyper-swe/mtix/internal/store"; type Storage=backend.Store;func f(Storage NodeWriter){Storage.CancelNode(nil,"","","",false)}`,
		`package sample; import backend "github.com/hyper-swe/mtix/internal/store"; type Holder struct{Store NodeWriter};func f(backend Holder){backend.Store.CancelNode(nil,"","","",false)}`,
		`package sample; type CancelAPI interface{CancelNode(...any)error};func f(CancelAPI func(any)NodeWriter, svc NodeWriter){writer:=CancelAPI(svc);writer.CancelNode(nil,"","","",false)}`,
	} {
		require.Empty(t, directStoreMutations("guard.go", source))
	}
}

// Required accidental scope includes package aliases declared in a different
// file, not just the same-file local bindings exercised above.
func TestServiceLayer_GuardSupportedDirectLocalAndPackageAliases(t *testing.T) {
	for _, body := range []string{
		`packageStore.CancelNode(nil,"","","",false)`,
		`db:=packageStore;db.CancelNode(nil,"","","",false)`,
		`packageAlias.CancelNode(nil,"","","",false)`,
		`cancel:=packageAlias.CancelNode;cancel(nil,"","","",false)`,
	} {
		sources := map[string]string{
			"backend.go": `package sample; import backend "github.com/hyper-swe/mtix/internal/store";var packageStore backend.Store`,
			"alias.go":   `package sample; var packageAlias = packageStore`,
			"handler.go": `package sample;func f(){` + body + `}`,
		}
		require.NotEmpty(t, directPackageStoreMutations(sources), body)
	}
	sources := map[string]string{
		"backend.go": `package sample;import backend "github.com/hyper-swe/mtix/internal/store";var packageStore backend.Store;var writer NodeWriter`,
		"alias.go":   `package sample;var packageAlias=packageStore;var serviceAlias=writer`,
		"handler.go": `package sample;func f(){packageAlias.GetNode(nil,"");serviceAlias.CancelNode(nil,"","","",false)}`,
	}
	require.Empty(t, directPackageStoreMutations(sources))
}

// Characterize currently unsupported deliberate evasions under ruling3345.
// Empty findings here document known S3 limits, not a code fix for old F2.
// Reassigned conditional/loop/closure aliases, reflection and other complex
// flows are also outside the supported guarantee; some are already detected.
// Do not infer that every out-of-scope form must bypass the existing detector.
func TestServiceLayer_GuardKnownDeliberateEvasionLimits(t *testing.T) {
	for _, body := range []string{
		`db:=any(s.store).(backend.Store);db.CancelNode(nil,"","","",false)`,
		`db,ok:=any(s.store).(backend.Store);if ok{db.CancelNode(nil,"","","",false)}`,
		`switch db:=any(s.store).(type){case backend.Store:db.CancelNode(nil,"","","",false)}`,
		`db:=[]backend.Store{s.store}[0];db.CancelNode(nil,"","","",false)`,
		`db:=struct{storage backend.Store}{s.store}.storage;db.CancelNode(nil,"","","",false)`,
		`db:=func()backend.Store{return s.store}();db.CancelNode(nil,"","","",false)`,
	} {
		source := `package sample;import backend "github.com/hyper-swe/mtix/internal/store";type Server struct{store backend.Store};func(s *Server)f(){` + body + `}`
		require.Empty(t, directStoreMutations("known-limit.go", source), body)
	}
}
