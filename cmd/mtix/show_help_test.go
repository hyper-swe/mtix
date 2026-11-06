// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestShowHelp_LabelTable_MatchesPrintedLabelsInOrder(t *testing.T) {
	initShowHelpApp(t)
	ctx := context.Background()
	node, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "TEST", Title: "Fully populated", Description: "A description",
		Prompt: strings.Repeat("界", 101), Creator: "tester", IssueType: model.IssueTypeTask,
	})
	require.NoError(t, err)
	require.NoError(t, app.store.ClaimNode(ctx, node.ID, "agent-x"))
	captureStdout(t, func() { require.NoError(t, runAnnotate(node.ID, "note")) })
	for _, history := range []bool{false, true} {
		out := captureStdout(t, func() {
			require.NoError(t, runShowWith(node.ID, showOptions{history: history, historyLimit: 50}))
		})
		help := newShowCmd().Long
		table := strings.SplitN(help, "\n\n", 3)
		require.Len(t, table, 3)
		documented := showHelpLabels(table[1], `(?m)^  ([A-Z][A-Za-z ]*?)  +`)
		require.NotEmpty(t, documented)
		if !history {
			require.Equal(t, "History", documented[len(documented)-1])
			documented = documented[:len(documented)-1]
		}
		printed := showHelpLabels(out, `(?m)^([A-Z][A-Za-z ]*):`)
		assert.Equal(t, documented, printed, "help table and output must match in both directions")
		assert.Contains(t, help, "longer prompts are cut to 97 characters followed by ...")
		assert.Contains(t, out, "Prompt:   "+strings.Repeat("界", 97)+"...\n")
		assert.Contains(t, help, "2006-01-02 15:04 (UTC, no zone)")
		assert.Contains(t, out, "Created:  "+node.CreatedAt.UTC().Format("2006-01-02 15:04")+"\n")
		assert.Contains(t, help, "for example")
		assert.Contains(t, help, "--json")
	}
}

func showHelpLabels(text, pattern string) []string {
	var labels []string
	for _, match := range regexp.MustCompile(pattern).FindAllStringSubmatch(text, -1) {
		labels = append(labels, match[1])
	}
	return labels
}

// This is a guard test: the store already returns UTC, so it passes on the base
// and pins the documented format against a future non-UTC return.
func TestShowHelp_CreatedTimestamp_PrintsUTCWithoutZone(t *testing.T) {
	initShowHelpApp(t)
	node := &model.Node{ID: "TEST-1", Project: "TEST", Title: "Creation time",
		Status: model.StatusOpen, CreatedAt: time.Date(2026, 9, 3, 8, 9, 59, 0, time.FixedZone("offset", 3600)),
	}
	require.NoError(t, app.store.CreateNode(context.Background(), node))
	out := showOutput(t, node.ID)
	assert.Contains(t, out, "Created:  2026-09-03 07:09\n")
}

// initShowHelpApp supplies a deterministic sync identity without inspecting host interfaces.
func initShowHelpApp(t *testing.T) {
	t.Helper()
	initTestApp(t)
	_, err := app.store.WriteDB().ExecContext(context.Background(),
		"INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", "meta.sync.machine_hash", "0123456789abcdef")
	require.NoError(t, err)
}

func TestShowHelp_PromptBoundaryLengths_MatchDocumentedLimit(t *testing.T) {
	for _, length := range []int{97, 99, 100, 101} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			initShowHelpApp(t)
			prompt := strings.Repeat("🙂", length)
			node, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{
				Project: "TEST", Title: "Prompt boundary", Creator: "tester", Prompt: prompt,
			})
			require.NoError(t, err)
			want := prompt
			if length > 100 {
				want = strings.Repeat("🙂", 97) + "..."
			}
			assert.Contains(t, newShowCmd().Long, "up to 100 characters (Unicode code points)")
			assert.Contains(t, showOutput(t, node.ID), "Prompt:   "+want+"\n")
		})
	}
}
