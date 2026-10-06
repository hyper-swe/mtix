// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Partial classification changes preserve omission and allow explicit clearing (MTIX-107.60).
package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestUpdateCLI_IssueType_PartialValidated(t *testing.T) {
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", "", "epic", "BUG", " bug"} {
		t.Run(kind, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Classified work", "", "feature", 3, "", "", "", "", ""))
			cmd := newUpdateCmd()
			cmd.SetArgs([]string{"TEST-1", "--type=" + kind})
			err := cmd.Execute()
			if kind == "epic" || kind == "BUG" || kind == " bug" {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				n, e := app.store.GetNode(context.Background(), "TEST-1")
				require.NoError(t, e)
				assert.Equal(t, model.IssueTypeFeature, n.IssueType)
				return
			}
			require.NoError(t, err)
			n, e := app.store.GetNode(context.Background(), "TEST-1")
			require.NoError(t, e)
			assert.Equal(t, model.IssueType(kind), n.IssueType)
			cmd = newUpdateCmd()
			cmd.SetArgs([]string{"TEST-1", "--title=Updated title"})
			require.NoError(t, cmd.Execute())
			n, e = app.store.GetNode(context.Background(), "TEST-1")
			require.NoError(t, e)
			assert.Equal(t, model.IssueType(kind), n.IssueType)
			assert.Equal(t, model.NodeTypeEpic, n.NodeType)
		})
	}
}

func TestListCLI_IssueType_FilterAndStructuralType(t *testing.T) {
	cases := []struct {
		args    []string
		ids     []string
		invalid bool
	}{
		{[]string{"--issue-type=bug"}, []string{"TEST-1"}, false},
		{[]string{"--issue-type=bug,doc"}, []string{"TEST-1", "TEST-2"}, false},
		{[]string{"--type=epic", "--issue-type=doc"}, []string{"TEST-2"}, false},
		{[]string{"--type=story", "--issue-type=doc"}, []string{}, false},
		{[]string{"--issue-type=feature"}, []string{}, false},
		{[]string{"--issue-type=epic"}, nil, true},
		{[]string{"--issue-type=bug,invalid"}, nil, true},
	}
	for _, tt := range cases {
		t.Run(tt.args[0], func(t *testing.T) {
			initTestApp(t)
			app.jsonOutput = true
			for _, kind := range []string{"bug", "doc", ""} {
				captureStdout(t, func() { require.NoError(t, runCreate("Classified work", "", kind, 3, "", "", "", "", "")) })
			}
			cmd := newListCmd()
			cmd.SetArgs(tt.args)
			var err error
			raw := captureStdout(t, func() { err = cmd.Execute() })
			if tt.invalid {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				return
			}
			require.NoError(t, err)
			var result struct {
				Nodes []model.Node
				Total int
			}
			require.NoError(t, json.Unmarshal([]byte(raw), &result))
			ids := make([]string, 0, len(result.Nodes))
			for _, n := range result.Nodes {
				ids = append(ids, n.ID)
			}
			assert.Equal(t, tt.ids, ids)
			assert.Equal(t, len(tt.ids), result.Total)
		})
	}
}

func TestClassificationCLI_HelpPinsAsymmetry(t *testing.T) {
	for _, cmd := range []*struct{ name, usage string }{
		{"create", newCreateCmd().Flags().Lookup("type").Usage},
		{"list", newListCmd().Flags().Lookup("type").Usage},
	} {
		if cmd.name == "list" {
			assert.Equal(t, "Filter by hierarchy node type (epic, story, issue, micro; comma-separated); use --issue-type for work classification", cmd.usage)
		} else {
			assert.Equal(t, "Issue type (bug, feature, task, chore, refactor, test, doc; omitted = unset); list --type filters hierarchy", cmd.usage)
		}
	}
	update := newUpdateCmd().Flags().Lookup("type")
	require.NotNil(t, update)
	assert.Equal(t, "New work classification (bug, feature, task, chore, refactor, test, doc); empty clears, omission preserves; list --type filters hierarchy", update.Usage)
	issue := newListCmd().Flags().Lookup("issue-type")
	require.NotNil(t, issue)
	assert.Equal(t, "Filter by work classification (bug, feature, task, chore, refactor, test, doc; comma-separated)", issue.Usage)
}

func TestListCLI_IssueType_DefaultAndProjectedOutput(t *testing.T) {
	initTestApp(t)
	captureStdout(t, func() {
		require.NoError(t, runCreate("Classified work", "", "refactor", 3, "", "", "", "", ""))
		require.NoError(t, runCreate("Unclassified work", "", "", 3, "", "", "", "", ""))
	})
	text := captureStdout(t, func() { require.NoError(t, runList("", "", "", "", "", "", "", "", 0, false, 50, "", false)) })
	assert.Contains(t, text, "Issue type")
	assert.Contains(t, text, "refactor")
	assert.Contains(t, text, "epic")
	app.jsonOutput = true
	for _, fields := range []string{"", "id,node_type,issue_type"} {
		raw := captureStdout(t, func() { require.NoError(t, runList("", "", "", "", "", fields, "", "", 0, false, 50, "", false)) })
		var result struct{ Nodes []map[string]any }
		require.NoError(t, json.Unmarshal([]byte(raw), &result))
		require.Len(t, result.Nodes, 2)
		assert.Equal(t, "refactor", result.Nodes[0]["issue_type"])
		assert.Equal(t, "epic", result.Nodes[0]["node_type"])
		if fields == "" {
			assert.NotContains(t, result.Nodes[1], "issue_type")
		} else {
			assert.Equal(t, "", result.Nodes[1]["issue_type"])
		}
	}
}
