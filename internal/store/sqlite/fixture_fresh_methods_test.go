// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"strings"
	"testing"
)

// Method fixtures exercise source-reference dispatch, including method values,
// while separate-file fixtures pin discovery of future initialization tests.
func TestFreshFixtureRouting_MethodRoutes(t *testing.T) {
	cases := []struct{ name, body, extra, want string }{
		{"method-call-image", "_ = newFreshTestStore; _ = helper{}.store(t)", "type helper struct{}; func (helper) store(t *testing.T) *sqlite.Store { return newTestStore(t) }", "image"},
		{"method-value-image", "_ = newFreshTestStore; ctor := helper{}.store; _ = ctor(t)", "type helper struct{}; func (helper) store(t *testing.T) *sqlite.Store { return newTestStore(t) }", "image"},
		{"method-expression-image", "_ = newFreshTestStore; _ = helper.store(helper{}, t)", "type helper struct{}; func (helper) store(t *testing.T) *sqlite.Store { return newTestStore(t) }", "image"},
		{"pointer-method-expression-image", "_ = newFreshTestStore; _ = (*helper).store(&helper{}, t)", "type helper struct{}; func (*helper) store(t *testing.T) *sqlite.Store { return newTestStore(t) }", "image"},
		{"receiver-method-chain-image", "_ = newFreshTestStore; _ = helper{}.store(t)", "type helper struct{}; func (h helper) store(t *testing.T) *sqlite.Store { return h.image(t) }; func (helper) image(t *testing.T) *sqlite.Store { return newTestStore(t) }", "image"},
		{"method-fresh", "_ = helper{}.store(t)", "type helper struct{}; func (helper) store(t *testing.T) *sqlite.Store { return newFreshTestStore(t) }", ""},
		{"receiver-shadows-global-helper", "_ = helper{}.store(t)", "type helper struct{}; func (newTestStore helper) store(t *testing.T) *sqlite.Store { _ = newTestStore; return newFreshTestStore(t) }", ""},
		{"method-name-is-not-package-function", "_ = newFreshTestStore(t)", "type helper struct{}; func (helper) newFreshTestStore(t *testing.T) *sqlite.Store { return newTestStore(t) }", ""},
		{"import-constructor-is-not-local-method", `_, _ = sqlite.New("new.db", nil)`, "type helper struct{}; func (helper) New(t *testing.T) *sqlite.Store { return newTestStore(t) }", ""},
		{"ambiguous-receiver-fails-closed", "_ = helper{}.store(t)", "type helper struct{}; type other struct{}; func (helper) store(t *testing.T) *sqlite.Store { return newFreshTestStore(t) }; func (other) store(t *testing.T) *sqlite.Store { return newTestStore(t) }", "image"},
		{"method-cycle-fails-closed", "_ = helper{}.store(t)", "type helper struct{}; func (h helper) store(t *testing.T) *sqlite.Store { return h.store(t) }", "fresh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sources := fixtureRoutingExample("TestPinnedSubject", tc.body, tc.extra)
			required := []freshFixtureSubject{{"store_test.go", "TestPinnedSubject"}}
			assertFixtureRoutingDiagnostic(t, sources, required, "TestPinnedSubject", tc.want)
		})
	}
}

func TestFreshFixtureRouting_NewBackupFile(t *testing.T) {
	cases := []struct{ name, subject, body, want string }{
		{"direct-image-rejected", "TestBackup_Future", `_ = sqlite.FixtureStoreForTest(t, "image.db")`, "image"},
		{"fresh-permitted", "TestBackup_Future", `_, _ = sqlite.New("new.db", nil)`, ""},
		{"ordinary-image-permitted", "TestCreateNode_Future", `_ = sqlite.FixtureStoreForTest(t, "image.db")`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sources := fixtureRoutingExample("TestOrdinarySubject", "_ = t", "")
			sources["backup_future_test.go"] = []byte(fmt.Sprintf(`package sqlite_test
import ("testing"; sqlite "github.com/hyper-swe/mtix/internal/store/sqlite")
func %s(t *testing.T) { %s }
`, tc.subject, tc.body))
			assertFixtureRoutingDiagnostic(t, sources, nil, tc.subject, tc.want)
		})
	}
}

func assertFixtureRoutingDiagnostic(t *testing.T, sources map[string][]byte, required []freshFixtureSubject, subject, want string) {
	t.Helper()
	problems := freshFixtureRoutingProblems(sources, required)
	if want == "" {
		if len(problems) != 0 {
			t.Fatalf("valid routing rejected: %v", problems)
		}
		return
	}
	for _, problem := range problems {
		if problem.name == subject && strings.Contains(problem.message, want) {
			return
		}
	}
	t.Fatalf("missing actionable %s diagnostic for %s: %v", want, subject, problems)
}
