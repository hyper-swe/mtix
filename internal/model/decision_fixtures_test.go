// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

const decisionFixtureDir = "../../docs/schemas/fixtures"

type decisionExpect struct {
	Class         string `json:"class"`
	Kind          string `json:"kind"`
	HadDuplicates bool   `json:"had_duplicates"`
	Canonical     string `json:"canonical"`
	PayloadSHA256 string `json:"payload_sha256"`
	ContentSHA256 string `json:"content_sha256"`
	SameHashAs    string `json:"same_hash_as"`
}

func loadDecisionFixture(t *testing.T, name string) ([]byte, decisionExpect) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(decisionFixtureDir, name+".json"))
	require.NoError(t, err)
	var exp decisionExpect
	b, err := os.ReadFile(filepath.Join(decisionFixtureDir, name+".expect.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &exp))
	return raw, exp
}

func decisionFixtureNames(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(decisionFixtureDir, "*.expect.json"))
	require.NoError(t, err)
	var names []string
	for _, f := range files {
		names = append(names, strings.TrimSuffix(filepath.Base(f), ".expect.json"))
	}
	require.NotEmpty(t, names, "no fixtures found")
	return names
}

func TestIngestDecision_GoldenFixtures_ClassAndHash(t *testing.T) {
	for _, name := range decisionFixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			raw, exp := loadDecisionFixture(t, name)
			res := model.IngestDecision(raw)
			assert.Equal(t, model.DecisionClass(exp.Class), res.Class, res.Reason)
			assert.Equal(t, model.DecisionKind(exp.Kind), res.Kind)
			assert.Equal(t, exp.HadDuplicates, res.HadDuplicates)
			assert.Equal(t, exp.Canonical, string(res.Canonical))
			assert.Equal(t, exp.PayloadSHA256, res.PayloadSHA256)
			assert.Equal(t, exp.ContentSHA256, res.ContentSHA256)
			if exp.Class == "valid" {
				require.NotNil(t, res.Value)
				assert.Equal(t, model.DecisionKind(exp.Kind), res.Value.Kind)
			} else {
				assert.Nil(t, res.Value)
			}
		})
	}
}

func TestIngestDecision_FixturesWithDuplicates_HashLikeTheirCleanTwin(t *testing.T) {
	pairs := 0
	for _, name := range decisionFixtureNames(t) {
		raw, exp := loadDecisionFixture(t, name)
		if exp.SameHashAs == "" {
			continue
		}
		pairs++
		twinRaw, twin := loadDecisionFixture(t, exp.SameHashAs)
		a, b := model.IngestDecision(raw), model.IngestDecision(twinRaw)
		assert.True(t, a.HadDuplicates, name)
		assert.False(t, b.HadDuplicates, exp.SameHashAs)
		assert.Equal(t, a.PayloadSHA256, b.PayloadSHA256, name)
		assert.Equal(t, twin.PayloadSHA256, a.PayloadSHA256, name)
		assert.Equal(t, a.Value, b.Value, name)
	}
	assert.GreaterOrEqual(t, pairs, 4, "expected fixtures with duplicates in ticket, source, options[] and the top level")
}

func TestIngestDecision_FixtureCoverage_EveryClassAndKind(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range decisionFixtureNames(t) {
		_, exp := loadDecisionFixture(t, name)
		seen[exp.Class+"/"+exp.Kind] = true
	}
	for _, k := range []string{"request", "answer", "outcome", "withdraw"} {
		for _, c := range []string{"valid", "malformed"} {
			assert.True(t, seen[c+"/"+k], "missing fixture %s %s", c, k)
		}
	}
	for _, k := range []string{"request", "answer", "outcome"} {
		assert.True(t, seen["unsupported/"+k], "missing unsupported fixture %s", k)
	}
}
