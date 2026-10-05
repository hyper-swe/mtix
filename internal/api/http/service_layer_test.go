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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// Layering guards pin the HTTP service boundary and mutation-only boundary
// of gRPC/MCP. Their reads are tracked separately by MTIX-127.
func TestServiceLayer_HTTPHasNoStoreAndTransportsHaveNoDirectMutations(t *testing.T) {
	for _, dir := range []string{".", "../grpc", "../../mcp"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		require.NoError(t, err)
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			content, err := os.ReadFile(file)
			require.NoError(t, err)
			text := string(content)
			if dir == "." {
				require.NotContains(t, text, `"github.com/hyper-swe/mtix/internal/store`, file)
			}
			require.Empty(t, directStoreMutations(file, text), "direct backend mutation in %s", file)
		}
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

// directStoreMutations recognizes storage-typed variables/fields and aliases,
// so changing a receiver name cannot defeat the recurrence guard.
func directStoreMutations(file, source string) []string {
	parsed, err := parser.ParseFile(token.NewFileSet(), file, source, 0)
	if err != nil {
		return []string{err.Error()}
	}
	aliases := map[string]bool{}
	for _, imp := range parsed.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if path == "github.com/hyper-swe/mtix/internal/store" || strings.HasPrefix(path, "github.com/hyper-swe/mtix/internal/store/") {
			name := filepath.Base(path)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			aliases[name] = true
		}
	}
	bindings := backendBindings(parsed, aliases)
	var findings []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if mutationMethod(selector.Sel.Name) && backendExpression(selector.X, bindings) {
			findings = append(findings, selector.Sel.Name)
		}
		return true
	})
	return findings
}
func backendType(expr ast.Expr, aliases map[string]bool) bool {
	switch v := expr.(type) {
	case *ast.StarExpr:
		return backendType(v.X, aliases)
	case *ast.SelectorExpr:
		id, ok := v.X.(*ast.Ident)
		return ok && aliases[id.Name]
	case *ast.Ident:
		return strings.HasSuffix(v.Name, "Store")
	}
	return false
}
func backendBindings(file *ast.File, aliases map[string]bool) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Field:
			if backendType(x.Type, aliases) {
				for _, id := range x.Names {
					names[id.Name] = true
				}
			}
		case *ast.ValueSpec:
			if backendType(x.Type, aliases) {
				for _, id := range x.Names {
					names[id.Name] = true
				}
			}
		}
		return true
	})
	for round := 0; round < 3; round++ {
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, rhs := range assign.Rhs {
				if i < len(assign.Lhs) && backendExpression(rhs, names) {
					if id, ok := assign.Lhs[i].(*ast.Ident); ok {
						names[id.Name] = true
					}
				}
			}
			return true
		})
	}
	return names
}
func backendExpression(expr ast.Expr, names map[string]bool) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		return names[x.Name]
	case *ast.SelectorExpr:
		return names[x.Sel.Name] || backendExpression(x.X, names)
	}
	return false
}
func mutationMethod(name string) bool {
	switch name {
	case "ClaimNode", "ForceReclaimNode", "UnclaimNode", "CancelNode", "SetAnnotations", "Backup", "AddDependency", "RemoveDependency", "InboxAck", "CreateNode", "UpdateNode", "DeleteNode", "UndeleteNode", "TransitionStatus", "DeferNode", "UpdateProgress", "NextSequence", "WriteDB", "WithTx", "Exec", "ExecContext":
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
