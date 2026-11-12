// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func seedLabelled(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, c := range []struct {
		title  string
		labels []string
	}{
		{"Now alpha", []string{"desk:now"}},
		{"Backlog beta", []string{"desk:backlog", "extra"}},
		{"Plain gamma", nil},
	} {
		_, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
			Project: "TEST", Title: c.title, Creator: "tester", Labels: c.labels,
		})
		require.NoError(t, err)
	}
}

// runLabelCmd runs a list or search command with args and returns node titles.
func runLabelCmd(t *testing.T, name string, args ...string) []string {
	t.Helper()
	app.jsonOutput = true
	cmd := newListCmd()
	if name == "search" {
		cmd = newSearchCmd()
	}
	cmd.SetArgs(args)
	var execErr error
	out := captureStdout(t, func() { execErr = cmd.Execute() })
	require.NoError(t, execErr)
	var got struct {
		Nodes []struct {
			Title string `json:"title"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	titles := []string{}
	for _, n := range got.Nodes {
		titles = append(titles, n.Title)
	}
	return titles
}

// MTIX-255.4: --labels is any-of on list and on search, colon labels match literally.
func TestListSearchCmd_LabelsFlag_AnyOfMatch(t *testing.T) {
	initTestApp(t)
	seedLabelled(t)
	for _, name := range []string{"list", "search"} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, []string{"Now alpha"}, runLabelCmd(t, name, "--labels", "desk:now"))
			assert.ElementsMatch(t, []string{"Now alpha", "Backlog beta"},
				runLabelCmd(t, name, "--labels", "desk:now,desk:backlog"))
			assert.ElementsMatch(t, []string{"Now alpha", "Backlog beta"},
				runLabelCmd(t, name, "--labels", " desk:now , ,desk:backlog "))
			assert.Empty(t, runLabelCmd(t, name, "--labels", "missing"))
			assert.Len(t, runLabelCmd(t, name, "--labels", ""), 3)
			assert.Len(t, runLabelCmd(t, name, "--labels", " , "), 3, "blank entries mean no filter")
			assert.Empty(t, runLabelCmd(t, name, "--labels", "desk:now", "--status", "done"))
			assert.ElementsMatch(t, []string{"Now alpha", "Backlog beta"},
				runLabelCmd(t, name, "--labels", "desk:now,desk:backlog", "--status", "open"))
		})
	}
}

// MTIX-255.4: search words and --labels combine by AND.
func TestSearchCmd_LabelsFlag_CombinesWithWords(t *testing.T) {
	initTestApp(t)
	seedLabelled(t)
	assert.Equal(t, []string{"Now alpha"}, runLabelCmd(t, "search", "alpha", "--labels", "desk:now,desk:backlog"))
	assert.Empty(t, runLabelCmd(t, "search", "alpha", "--labels", "desk:backlog"))
}
