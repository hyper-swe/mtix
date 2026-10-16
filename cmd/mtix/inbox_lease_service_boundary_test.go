// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWakeLeaseBoundary_Production_UsesServices(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var files []*ast.File
	for _, file := range paths {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		require.NoError(t, parseErr)
		files = append(files, parsed)
	}
	require.Empty(t, leaseBoundaryViolations(fset, files), "CLI wake leases must go through services, including inactive build-tag files")
}

type leaseBoundaryCase struct {
	name, source string
	violations   int
}

func leaseBoundaryFixture(body string) string {
	return "package sample\nimport db \"github.com/hyper-swe/mtix/internal/store/sqlite\"\nimport svc \"github.com/hyper-swe/mtix/internal/service\"\n" + body
}

func leaseBoundaryStorageCases() []leaseBoundaryCase {
	return []leaseBoundaryCase{
		{"direct field", leaseBoundaryFixture("type appContext struct {store *db.Store}; var app appContext; func f(){app.store.AcquireWakeLease()}"), 1},
		{"parenthesized alias", leaseBoundaryFixture("func f(s *db.Store){a:=s; b:=a; (b).ReleaseWakeLease()}"), 1},
		{"method value", leaseBoundaryFixture("func f(s *db.Store){call:=s.AcquireWakeLease; _=call}"), 1},
		{"method expression", leaseBoundaryFixture("var call=(*db.Store).ReleaseWakeLease"), 1},
		{"type alias", leaseBoundaryFixture("type backend = db.Store; func f(s *backend){s.AcquireWakeLease()}"), 1},
		{"constructor", leaseBoundaryFixture("func f(){s,_:=db.New(); s.ReleaseWakeLease()}"), 1},
		{"typed return", leaseBoundaryFixture("func backend()*db.Store{return nil}; func f(){backend().AcquireWakeLease()}"), 1},
		{"embedded store", leaseBoundaryFixture("type wrapper struct {*db.Store}; func f(s *wrapper){s.AcquireWakeLease()}"), 1},
		{"capability interface", "package sample; type backend interface {AcquireWakeLease()}; func f(s backend){s.AcquireWakeLease()}", 1},
		{"unresolved receiver", "package sample; func f(){unknown.ReleaseWakeLease()}", 1},
		{"dot import", "package sample; import . \"github.com/hyper-swe/mtix/internal/store/sqlite\"; func f(s *Store){s.ReleaseWakeLease()}", 1},
		{"raw import", "package sample; import db `github.com/hyper-swe/mtix/internal/store/sqlite`; func f(s *db.Store){s.AcquireWakeLease()}", 1},
		{"escaped import", "package sample; import db \"github.com/hyper-swe/mtix/internal/sto\\x72e/sqlite\"; func f(s *db.Store){s.AcquireWakeLease()}", 1},
	}
}

func leaseBoundaryNonStorageCases() []leaseBoundaryCase {
	return []leaseBoundaryCase{
		{"service receiver", leaseBoundaryFixture("func f(s *svc.WakeLeaseService){s.AcquireWakeLease()}"), 0},
		{"service constructor", leaseBoundaryFixture("func f(){s:=svc.NewWakeLeaseService(nil); s.ReleaseWakeLease()}"), 0},
		{"unrelated method", "package sample; type other struct{}; func(other)AcquireWakeLease(){};func f(s other){s.AcquireWakeLease()}", 0},
		{"shadowed alias", leaseBoundaryFixture("type other struct{};func(other)AcquireWakeLease(){};func f(s *db.Store){backend:=s; _=backend; {backend:=other{};backend.AcquireWakeLease()}}"), 0},
		{"nonembedded storage field", leaseBoundaryFixture("type other struct{db *db.Store};func(other)AcquireWakeLease(){};func f(s other){s.AcquireWakeLease()}"), 0},
	}
}

func TestWakeLeaseBoundary_Receivers_ClassifiesStorage(t *testing.T) {
	cases := append(leaseBoundaryStorageCases(), leaseBoundaryNonStorageCases()...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "fixture.go", tc.source, parser.SkipObjectResolution)
			require.NoError(t, err)
			require.Len(t, leaseBoundaryViolations(fset, []*ast.File{file}), tc.violations)
		})
	}
}

func TestWakeLeaseBoundary_BuildTags_ChecksInactiveSources(t *testing.T) {
	for _, tc := range []struct {
		name, parameter string
		want            int
	}{
		{"store", "db.Store", 2}, {"service", "svc.WakeLeaseService", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			var files []*ast.File
			for _, platform := range []string{"linux", "windows"} {
				source := "//go:build " + platform + "\n\n" + leaseBoundaryFixture("func handler(s *"+tc.parameter+"){s.AcquireWakeLease()}")
				file, err := parser.ParseFile(fset, "handler_"+platform+".go", source, parser.SkipObjectResolution)
				require.NoError(t, err)
				files = append(files, file)
			}
			require.Len(t, leaseBoundaryViolations(fset, files), tc.want)
		})
	}
}
