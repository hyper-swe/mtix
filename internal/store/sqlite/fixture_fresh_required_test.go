// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Real-tree checks require pinned subjects; policy examples can still exercise
// automatic initialization-family discovery with no explicit pins.
func requiredFreshFixtureRoutingProblems(sources map[string][]byte, required []freshFixtureSubject) []freshFixtureProblem {
	if len(required) == 0 {
		return []freshFixtureProblem{{message: "required fresh subject set is empty"}}
	}
	return freshFixtureRoutingProblems(sources, required)
}

func TestFreshFixtureRouting_RequiredSubjects(t *testing.T) {
	pinned := []freshFixtureSubject{{"store_test.go", "TestPinnedSubject"}}
	cases := []struct {
		name     string
		required []freshFixtureSubject
		body     string
		want     string
	}{
		{"nil", nil, "_ = newFreshTestStore(t)", "required fresh subject set is empty"},
		{"empty", []freshFixtureSubject{}, "_ = newFreshTestStore(t)", "required fresh subject set is empty"},
		{"valid pin", pinned, "_ = newFreshTestStore(t)", ""},
		{"pin is checked", pinned, "_ = t", "no recognized fresh constructor route"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sources := fixtureRoutingExample("TestPinnedSubject", tc.body, "")
			problems := requiredFreshFixtureRoutingProblems(sources, tc.required)
			if tc.want == "" {
				require.Empty(t, problems)
				return
			}
			require.NotEmpty(t, problems)
			require.Contains(t, fmt.Sprint(problems), tc.want)
		})
	}
}
